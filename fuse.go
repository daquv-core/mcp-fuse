package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// latestSupported is the newest MCP revision this server speaks: the `initialize` handshake and the optional
// Mcp-Session-Id. Revisions are dates, so they compare as strings.
const latestSupported = "2025-11-25"

func newerThanSupported(v string) bool {
	if len(v) != len("2006-01-02") || v[4] != '-' || v[7] != '-' {
		return false
	}
	return v > latestSupported
}

type Fuse struct {
	cfg         Config
	client      *http.Client
	log         *slog.Logger
	version     string
	routes      *routeCache
	listTimeout time.Duration
	primary     int // where requests with no owner of their own go: the stateful backend, else the first
}

// call is one inbound request while it is being served.
type call struct {
	in    *http.Request
	body  []byte
	env   envelope
	reqID string
	start time.Time
}

func newFuse(cfg Config, log *slog.Logger, version string) *Fuse {
	primary := 0
	for i, b := range cfg.Backends {
		if b.Stateful {
			primary = i
		}
	}
	return &Fuse{
		cfg: cfg,
		client: &http.Client{Transport: &http.Transport{
			// Backends are addressed explicitly; an environment proxy must not sit between them and us.
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			// No response-header timeout and no overall timeout: a tool call may run as long as it needs to.
		}},
		log:         log,
		version:     version,
		routes:      newRouteCache(60*time.Second, 1024),
		listTimeout: time.Duration(cfg.ListTimeoutSeconds) * time.Second,
		primary:     primary,
	}
}

func (f *Fuse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		writeFrame(w, http.StatusOK, map[string]string{"status": "ok", "version": f.version})
	case f.cfg.prmPath, "/.well-known/oauth-protected-resource":
		f.serveMetadata(w, r)
	case f.cfg.mcpPath:
		f.serveMCP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveMetadata answers RFC 9728 discovery. Which authorization server to log in at is a deployment setting.
func (f *Fuse) serveMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeFrame(w, http.StatusOK, map[string]any{
		"resource":                 f.cfg.Resource,
		"authorization_servers":    f.cfg.AuthorizationServers,
		"bearer_methods_supported": []string{"header"},
	})
}

func (f *Fuse) serveMCP(w http.ResponseWriter, r *http.Request) {
	// GET (a standing event stream) and DELETE (ending a session) are not offered; the specification allows 405.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A client that speaks the 2026-07-28 revision tries it first and falls back to the `initialize` handshake
	// when the server answers 400 with nothing that looks like a modern error. Serving such a request here
	// would merge whatever the backends make of a handshake-less call - without the stateful one - and hand
	// back a list that is quietly incomplete.
	if v := r.Header.Get("MCP-Protocol-Version"); newerThanSupported(v) {
		http.Error(w, "unsupported protocol version "+v+"; this server speaks "+latestSupported+" and earlier",
			http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeRPCError(w, http.StatusRequestEntityTooLarge, nil, codeInvalidRequest, "request too large")
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "batch requests are not supported")
		return
	}
	var env envelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParse, "parse error")
		return
	}

	reqID := r.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = newRequestID()
	}
	c := &call{in: r, body: body, env: env, reqID: reqID, start: time.Now()}

	switch {
	case env.Method == "":
		// A response to a request the server made. This server makes none.
		w.WriteHeader(http.StatusAccepted)
	case !env.isRequest():
		f.notify(w, c)
	case env.Method == "initialize":
		f.initialize(w, c)
	case env.Method == "ping":
		writeResult(w, env.ID, struct{}{})
	default:
		if _, ok := kinds[env.Method]; ok {
			f.list(w, c, env.Method)
			return
		}
		if listMethod, ok := callKinds[env.Method]; ok {
			f.route(w, c, listMethod)
			return
		}
		f.forward(w, c, f.primary, "")
	}
}

// notify passes a notification on and acknowledges it. "initialized" belongs to the one backend that holds
// a session; anything else (a cancellation, above all) goes to every backend, because the one that owns the
// request is not known here and a request id that means nothing to a backend is ignored by it.
func (f *Fuse) notify(w http.ResponseWriter, c *call) {
	var targets []int
	if c.env.Method == "notifications/initialized" {
		if f.cfg.Backends[f.primary].Stateful {
			targets = []int{f.primary}
		}
	} else {
		for i := range f.cfg.Backends {
			targets = append(targets, i)
		}
	}

	ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := f.outbound(ctx, i, c, c.body)
			if err != nil {
				return
			}
			resp, err := f.client.Do(req)
			if err != nil {
				f.log.Warn("notification not delivered", "backend", f.cfg.Backends[i].Name, "method", c.env.Method,
					"request_id", c.reqID, "error", errKind(err))
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	w.WriteHeader(http.StatusAccepted)
}

// fanout sends the same body to every backend at once. The replies are index-aligned with the backends.
func (f *Fuse) fanout(ctx context.Context, c *call, body []byte, id json.RawMessage) []reply {
	reps := make([]reply, len(f.cfg.Backends))
	var wg sync.WaitGroup
	for i := range f.cfg.Backends {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reps[i] = f.rpc(ctx, i, c, body, id)
		}(i)
	}
	wg.Wait()
	return reps
}

type initResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	Instructions    string         `json:"instructions"`
}

// initialize asks every backend and answers with what they have in common: the oldest protocol version any of
// them speaks, the union of their capabilities, and their instructions one after another under the backend's
// name. The session id handed back is the stateful backend's own.
func (f *Fuse) initialize(w http.ResponseWriter, c *call) {
	ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
	defer cancel()
	reps := f.fanout(ctx, c, c.body, c.env.ID)

	var (
		version string
		caps    = map[string]any{}
		parts   []string
		sid     string
		ok      int
		unauth  *reply
	)
	for i, rep := range reps {
		bk := f.cfg.Backends[i]
		if rep.err != nil {
			if rep.unauthorized && unauth == nil {
				r := rep
				unauth = &r
			}
			f.log.Warn("initialize failed", "backend", bk.Name, "status", rep.status, "request_id", c.reqID, "error", errText(rep.err))
			continue
		}
		var res initResult
		dec := json.NewDecoder(bytes.NewReader(rep.env.Result))
		dec.UseNumber()
		if err := dec.Decode(&res); err != nil {
			f.log.Warn("initialize result unreadable", "backend", bk.Name, "request_id", c.reqID)
			continue
		}
		ok++
		if res.ProtocolVersion != "" && (version == "" || res.ProtocolVersion < version) {
			version = res.ProtocolVersion
		}
		mergeInto(caps, res.Capabilities)
		if text := strings.TrimSpace(res.Instructions); text != "" {
			parts = append(parts, "["+bk.Name+"]\n"+text)
		}
		if s := rep.header.Get(sessionHeader); s != "" {
			if bk.Stateful {
				sid = s
			} else {
				f.log.Warn("backend issued a session but is not configured as stateful; the id is dropped", "backend", bk.Name)
			}
		}
	}
	if ok == 0 {
		f.listFailure(w, c, unauth)
		return
	}
	if f.cfg.Backends[f.primary].Stateful && sid == "" {
		f.log.Warn("the stateful backend gave no session; calls to it will be refused", "backend", f.cfg.Backends[f.primary].Name, "request_id", c.reqID)
	}

	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    caps,
		"serverInfo":      map[string]string{"name": "mcp-fuse", "version": f.version},
	}
	if len(parts) > 0 {
		result["instructions"] = strings.Join(parts, "\n\n")
	}
	if sid != "" {
		w.Header().Set(sessionHeader, sid)
	}
	writeResult(w, c.env.ID, result)
	f.log.Info("call", "method", "initialize", "backend", "*", "status", http.StatusOK,
		"ms", time.Since(c.start).Milliseconds(), "request_id", c.reqID)
}

// listFailure answers when no backend could be reached for a merged request: 401 if any of them asked the
// caller to authenticate, 502 otherwise.
func (f *Fuse) listFailure(w http.ResponseWriter, c *call, unauth *reply) {
	if unauth != nil {
		f.writeUnauthorized(w, c.env.ID, unauth.header)
		return
	}
	writeRPCError(w, http.StatusBadGateway, c.env.ID, codeBackend, "no backend available")
}

// mergeInto folds src into dst: objects are merged key by key, a flag is on if it is on in either, and for
// anything else the first value wins.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		cur, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		switch cv := cur.(type) {
		case map[string]any:
			if sv, ok := v.(map[string]any); ok {
				mergeInto(cv, sv)
			}
		case bool:
			if sv, ok := v.(bool); ok && sv {
				dst[k] = true
			}
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
