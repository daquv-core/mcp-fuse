package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
)

// A backend sees mcp-fuse as one MCP client. Each person has, per backend, their own token (oauth.go) and their
// own MCP session, which mcp-fuse opens and keeps; the caller never learns that a session exists. Nothing the
// caller sent is copied into the request except the JSON-RPC body: the headers are the ones an MCP client sends
// (and a request id), built here, so that a backend that checks what it receives has nothing unusual to check.

const (
	maxBody       = 16 << 20 // largest request body read
	maxReply      = 32 << 20 // largest backend reply read in full (lists, initialize)
	sessionHeader = "Mcp-Session-Id"
)

// reqIDKey carries the request id through the contexts of the calls made for one inbound request.
type reqIDKey struct{}

type sessionKey struct {
	person string
	idx    int
}

// bsession is the MCP session mcp-fuse holds with one backend for one person. id is empty for a backend that
// keeps no session; the version is the revision that backend agreed to.
type bsession struct {
	id      string
	version string
}

const maxSessions = 10000

func (f *Fuse) session(k sessionKey) (bsession, bool) {
	f.sessMu.Lock()
	defer f.sessMu.Unlock()
	s, ok := f.sessions[k]
	return s, ok
}

func (f *Fuse) setSession(k sessionKey, s bsession) {
	f.sessMu.Lock()
	defer f.sessMu.Unlock()
	if len(f.sessions) >= maxSessions {
		f.sessions = map[sessionKey]bsession{} // sessions are reopened on demand; losing them costs a handshake
	}
	f.sessions[k] = s
}

func (f *Fuse) dropSession(k sessionKey) {
	f.sessMu.Lock()
	defer f.sessMu.Unlock()
	delete(f.sessions, k)
}

func (f *Fuse) newRequest(ctx context.Context, idx int, body []byte, token string, s bsession) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.Backends[idx].URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	h.Set("User-Agent", "mcp-fuse/"+f.version)
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	if s.id != "" {
		h.Set(sessionHeader, s.id)
	}
	if s.version != "" {
		h.Set("MCP-Protocol-Version", s.version)
	}
	if id, _ := ctx.Value(reqIDKey{}).(string); id != "" {
		h.Set("X-Request-Id", id) // the one non-protocol header: it lets a backend's log be matched to ours
	}
	return req, nil
}

type opened struct {
	sess bsession
	env  envelope // the backend's answer to initialize
}

// open runs the MCP handshake with a backend as the person and remembers the session. It is called when a caller
// initializes and, lazily, when a session is missing or has been lost.
func (f *Fuse) open(ctx context.Context, person string, idx int, token string) (opened, error) {
	init, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": latestSupported,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "mcp-fuse", "version": f.version},
		},
	})
	req, err := f.newRequest(ctx, idx, init, token, bsession{})
	if err != nil {
		return opened{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return opened{}, errors.New(errKind(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return opened{}, errUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		return opened{}, fmt.Errorf("initialize: status %d", resp.StatusCode)
	}
	env, err := readFrame(resp, json.RawMessage("1"))
	if err != nil {
		return opened{}, err
	}
	if len(env.Error) > 0 {
		var e rpcError
		_ = json.Unmarshal(env.Error, &e)
		return opened{}, fmt.Errorf("initialize: rpc error %d", e.Code)
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.ProtocolVersion == "" {
		return opened{}, errors.New("initialize: no protocol version in the answer")
	}
	if res.ProtocolVersion > latestSupported {
		return opened{}, fmt.Errorf("initialize: backend speaks %s, which is newer than this server supports", res.ProtocolVersion)
	}
	sess := bsession{id: resp.Header.Get(sessionHeader), version: res.ProtocolVersion}

	// "initialized" completes the handshake. A failure here is not fatal: backends that keep no session ignore it.
	note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if nreq, err := f.newRequest(ctx, idx, note, token, sess); err == nil {
		if nresp, err := f.client.Do(nreq); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(nresp.Body, 1<<20))
			nresp.Body.Close()
		}
	}
	f.setSession(sessionKey{person, idx}, sess)
	return opened{sess: sess, env: env}, nil
}

// ensureSession returns the session for the person at the backend, opening one if there is none.
func (f *Fuse) ensureSession(ctx context.Context, person string, idx int, token string) (bsession, error) {
	key := sessionKey{person, idx}
	if s, ok := f.session(key); ok {
		return s, nil
	}
	mu := f.keyLock("session|" + person + "|" + strconv.Itoa(idx))
	mu.Lock()
	defer mu.Unlock()
	if s, ok := f.session(key); ok {
		return s, nil
	}
	o, err := f.open(ctx, person, idx, token)
	return o.sess, err
}

// withToken runs fn with the person's token for the backend. If the backend refuses the token, it is refreshed
// once and fn runs again; if it is refused again the grant is dropped and the person has to connect again.
func withToken[T any](f *Fuse, ctx context.Context, person string, idx int, fn func(token string) (T, error)) (T, error) {
	var zero T
	bk := f.cfg.Backends[idx]
	token := ""
	if bk.oauth() {
		t, err := f.accessToken(ctx, person, idx)
		if err != nil {
			return zero, err
		}
		token = t
	}
	for attempt := 0; ; attempt++ {
		v, err := fn(token)
		if !errors.Is(err, errUnauthorized) {
			return v, err
		}
		if !bk.oauth() {
			return zero, errors.New("backend refused a request that carried no token")
		}
		if attempt == 0 {
			t, rerr := f.refresh(ctx, person, idx, token)
			if rerr != nil {
				return zero, rerr
			}
			token = t
			continue
		}
		_ = f.store.deleteGrant(person, bk.Name)
		f.dropSession(sessionKey{person, idx})
		return zero, errNotConnected
	}
}

// do sends one request body to a backend as the person. With create false it does nothing if no session has
// been opened yet (a cancellation has nothing to cancel then).
func (f *Fuse) do(ctx context.Context, person string, idx int, body []byte, create bool) (*http.Response, error) {
	key := sessionKey{person, idx}
	return withToken(f, ctx, person, idx, func(token string) (*http.Response, error) {
		for try := 0; try < 2; try++ {
			sess, have := f.session(key)
			if !have {
				if !create {
					return nil, errNoSession
				}
				s, err := f.ensureSession(ctx, person, idx, token)
				if err != nil {
					return nil, err
				}
				sess = s
			}
			req, err := f.newRequest(ctx, idx, body, token, sess)
			if err != nil {
				return nil, err
			}
			resp, err := f.client.Do(req)
			if err != nil {
				return nil, errors.New(errKind(err))
			}
			switch {
			case resp.StatusCode == http.StatusUnauthorized:
				resp.Body.Close()
				return nil, errUnauthorized
			case resp.StatusCode == http.StatusNotFound && sess.id != "" && try == 0:
				// The backend no longer knows the session. It refused before acting, so asking again on a fresh
				// session is safe, and the caller - who never saw a session - need not hear of it.
				resp.Body.Close()
				f.dropSession(key)
				continue
			}
			return resp, nil
		}
		return nil, errors.New("session could not be restored")
	})
}

var errNoSession = errors.New("no session")

// connected says whether the person can be served by the backend right now: it asks for no token, or the person
// has connected it.
func (f *Fuse) connected(person string, idx int) bool {
	bk := f.cfg.Backends[idx]
	if !bk.oauth() {
		return true
	}
	_, ok := f.store.grant(person, bk.Name)
	return ok
}

// readFrame reads the response frame carrying wantID from a reply, JSON or event stream.
func readFrame(resp *http.Response, wantID json.RawMessage) (envelope, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return envelope{}, err
	}
	if len(data) > maxReply {
		return envelope{}, errors.New("reply too large")
	}
	var env envelope
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
		return rpcFromSSE(bytes.NewReader(data), wantID)
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return envelope{}, err
	}
	return env, nil
}
