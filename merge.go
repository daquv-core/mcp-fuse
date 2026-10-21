package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// kind describes one family of things a backend lists and a client then asks for by name.
type kind struct {
	key   string // member of the list result that holds the items
	field string // member of an item that names it
	param string // member of the follow-up request's params that names it
	noun  string
}

// kinds is keyed by the list method.
var kinds = map[string]kind{
	"tools/list":     {key: "tools", field: "name", param: "name", noun: "tool"},
	"prompts/list":   {key: "prompts", field: "name", param: "name", noun: "prompt"},
	"resources/list": {key: "resources", field: "uri", param: "uri", noun: "resource"},
}

// callKinds maps the follow-up method to the list method that says who owns the thing asked for.
var callKinds = map[string]string{
	"tools/call":     "tools/list",
	"prompts/get":    "prompts/list",
	"resources/read": "resources/list",
}

type listing struct {
	items       []json.RawMessage
	owner       map[string]int
	ok          int
	unauth      *reply
	sessionGone bool
}

type pages struct {
	items []json.RawMessage
	rep   reply
}

const maxPages = 20

// collect lists every backend as the caller, merges the answers in configuration order, and remembers who owns
// what. A backend that does not answer is left out; its tools come back when it does. When two backends offer
// the same name the earlier one in the configuration keeps it - both being dropped would take a working
// backend's tool away because of an unrelated one - and the clash is logged.
func (f *Fuse) collect(ctx context.Context, c *call, method string) listing {
	k := kinds[method]
	got := make([]pages, len(f.cfg.Backends))
	var wg sync.WaitGroup
	for i := range f.cfg.Backends {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i].items, got[i].rep = f.fetchAll(ctx, i, c, method, k)
		}(i)
	}
	wg.Wait()

	l := listing{items: []json.RawMessage{}, owner: map[string]int{}}
	for i, p := range got {
		bk := f.cfg.Backends[i]
		if p.rep.err != nil {
			if p.rep.sessionGone {
				l.sessionGone = true
			}
			if p.rep.unauthorized && l.unauth == nil {
				r := p.rep
				l.unauth = &r
			}
			f.log.Warn("list failed", "method", method, "backend", bk.Name, "status", p.rep.status,
				"request_id", c.reqID, "error", errText(p.rep.err))
			continue
		}
		l.ok++
		for _, raw := range p.items {
			var item map[string]json.RawMessage
			var name string
			if json.Unmarshal(raw, &item) == nil {
				_ = json.Unmarshal(item[k.field], &name)
			}
			if name != "" {
				if prev, taken := l.owner[name]; taken {
					f.log.Warn("name offered by two backends; the earlier one keeps it", k.noun, name,
						"kept", f.cfg.Backends[prev].Name, "dropped", bk.Name, "request_id", c.reqID)
					continue
				}
				l.owner[name] = i
			}
			l.items = append(l.items, raw)
		}
	}
	if l.ok > 0 {
		f.routes.put(routeKey{caller: callerKey(c.in), kind: method}, l.owner)
	}
	return l
}

// fetchAll reads every page of one backend's list. A backend that has no such method simply has none.
func (f *Fuse) fetchAll(ctx context.Context, idx int, c *call, method string, k kind) ([]json.RawMessage, reply) {
	var items []json.RawMessage
	cursor := ""
	var rep reply
	for page := 1; page <= maxPages; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": page, "method": method, "params": params})
		rep = f.rpc(ctx, idx, c, body, json.RawMessage(strconv.Itoa(page)))
		if rep.err != nil {
			if rep.methodMissing {
				rep.err = nil
				return items, rep
			}
			return nil, rep
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(rep.env.Result, &result); err != nil {
			rep.err = err
			return nil, rep
		}
		var chunk []json.RawMessage
		if raw, ok := result[k.key]; ok {
			if err := json.Unmarshal(raw, &chunk); err != nil {
				rep.err = err
				return nil, rep
			}
		}
		items = append(items, chunk...)
		cursor = ""
		if raw, ok := result["nextCursor"]; ok {
			_ = json.Unmarshal(raw, &cursor)
		}
		if cursor == "" {
			return items, rep
		}
	}
	f.log.Warn("list cut off", "method", method, "backend", f.cfg.Backends[idx].Name, "pages", maxPages, "request_id", c.reqID)
	return items, rep
}

func (f *Fuse) list(w http.ResponseWriter, c *call, method string) {
	ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
	defer cancel()
	l := f.collect(ctx, c, method)
	if l.sessionGone {
		f.writeSessionGone(w, c)
		return
	}
	if l.ok == 0 {
		f.listFailure(w, c, l.unauth)
		return
	}
	writeResult(w, c.env.ID, map[string]any{kinds[method].key: l.items})
	f.log.Info("call", "method", method, "backend", "*", "status", http.StatusOK,
		"ms", time.Since(c.start).Milliseconds(), "request_id", c.reqID)
}

// route sends a follow-up request (a tool call, a prompt, a resource read) to the backend that listed the
// thing it names, and relays the answer untouched.
func (f *Fuse) route(w http.ResponseWriter, c *call, listMethod string) {
	k := kinds[listMethod]
	name := paramString(c.env.Params, k.param)
	if name == "" {
		writeRPCError(w, http.StatusOK, c.env.ID, codeInvalidParams, "missing "+k.param)
		return
	}
	idx, ok := f.routes.get(routeKey{caller: callerKey(c.in), kind: listMethod}, name)
	if !ok {
		ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
		l := f.collect(ctx, c, listMethod)
		cancel()
		if l.sessionGone {
			f.writeSessionGone(w, c)
			return
		}
		if l.ok == 0 {
			f.listFailure(w, c, l.unauth)
			return
		}
		idx, ok = l.owner[name]
	}
	if !ok {
		writeRPCError(w, http.StatusOK, c.env.ID, codeInvalidParams, "unknown "+k.noun+": "+name)
		return
	}
	f.forward(w, c, idx, name)
}
