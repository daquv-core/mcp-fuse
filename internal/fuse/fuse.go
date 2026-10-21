package fuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daquv-core/mcp-fuse/internal/config"
	"github.com/daquv-core/mcp-fuse/internal/front"
	"github.com/daquv-core/mcp-fuse/internal/jsonrpc"
	"github.com/daquv-core/mcp-fuse/internal/oauthc"
	"github.com/daquv-core/mcp-fuse/internal/state"
)

// latestSupported is the newest MCP revision this server speaks: the `initialize` handshake and the optional
// Mcp-Session-Id. Revisions are dates, so they compare as strings.
const latestSupported = "2025-11-25"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newerThanSupported(v string) bool {
	if len(v) != len("2006-01-02") || v[4] != '-' || v[7] != '-' {
		return false
	}
	return v > latestSupported
}

// Deps are the parts of a Fuse that come from outside the configuration file.
type Deps struct {
	Store         *state.Store // nil when no backend needs a token
	Secret        []byte       // signs connect links
	Auth          front.Verifier
	ClientSecrets map[string]string // backend name -> secret of its pre-registered confidential client
	FrontSecret   string            // secret of the client mcp-fuse is at the callers' authorization server
}

// Fuse is the http.Handler that presents the configured backends as one MCP server.
type Fuse struct {
	cfg         config.Config
	client      *http.Client // MCP traffic to the backends; no overall timeout
	log         *slog.Logger
	version     string
	routes      *routeCache
	listTimeout time.Duration

	auth        front.Verifier
	store       *state.Store
	secret      []byte
	frontSecret string
	oc          *oauthc.Client

	sessMu   sync.Mutex
	sessions map[sessionKey]bsession
	locks    sync.Map // key -> *sync.Mutex

	pendMu  sync.Mutex
	pending map[string]pendingAuth
}

// call is one inbound request while it is being served.
type call struct {
	in     *http.Request
	body   []byte
	env    jsonrpc.Envelope
	person string // who the caller is, as the authorization server vouches
	reqID  string
	start  time.Time
}

// New builds the handler. The configuration must have been validated.
func New(cfg config.Config, log *slog.Logger, version string, d Deps) *Fuse {
	dial := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	f := &Fuse{
		cfg: cfg,
		client: &http.Client{Transport: &http.Transport{
			// Backends are addressed explicitly; an environment proxy must not sit between them and us.
			Proxy:               nil,
			DialContext:         dial.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			// No response-header timeout and no overall timeout: a tool call may run as long as it needs to.
		}},
		log:         log,
		version:     version,
		routes:      newRouteCache(60*time.Second, 1024),
		listTimeout: time.Duration(cfg.ListTimeoutSeconds) * time.Second,
		auth:        d.Auth,
		store:       d.Store,
		secret:      d.Secret,
		frontSecret: d.FrontSecret,
		sessions:    map[sessionKey]bsession{},
		pending:     map[string]pendingAuth{},
	}
	f.oc = &oauthc.Client{
		HTTP: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
			Proxy: nil, DialContext: dial.DialContext, TLSHandshakeTimeout: 10 * time.Second,
		}},
		Store: d.Store, Backends: cfg.Backends, Secrets: d.ClientSecrets, Version: version, Probe: latestSupported,
		RedirectURI: cfg.Origin + cfg.ConnectBase + callbackPath,
		OnRevoked:   func(person string, idx int) { f.dropSession(sessionKey{person, idx}) },
	}
	return f
}

// keyLock returns the one mutex for a key, so that work on the same person and backend is serialized.
func (f *Fuse) keyLock(key string) *sync.Mutex {
	mu, _ := f.locks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (f *Fuse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		jsonrpc.WriteFrame(w, http.StatusOK, map[string]string{"status": "ok", "version": f.version})
	case r.URL.Path == f.cfg.PRMPath || r.URL.Path == "/.well-known/oauth-protected-resource":
		f.serveMetadata(w, r)
	case strings.HasPrefix(r.URL.Path, f.cfg.ConnectBase+"/"):
		f.serveConnect(w, r)
	case r.URL.Path == f.cfg.MCPPath:
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
	jsonrpc.WriteFrame(w, http.StatusOK, map[string]any{
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
	// when the server answers 400 with nothing that looks like a modern error.
	if v := r.Header.Get("MCP-Protocol-Version"); newerThanSupported(v) {
		http.Error(w, "unsupported protocol version "+v+"; this server speaks "+latestSupported+" and earlier",
			http.StatusBadRequest)
		return
	}

	token := front.BearerToken(r)
	person, err := f.auth.Verify(r.Context(), token)
	switch {
	case errors.Is(err, front.ErrUnauthenticated):
		f.writeUnauthenticated(w, nil, token != "")
		return
	case err != nil:
		jsonrpc.WriteError(w, http.StatusServiceUnavailable, nil, jsonrpc.CodeBackend, "authentication unavailable")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		jsonrpc.WriteError(w, http.StatusRequestEntityTooLarge, nil, jsonrpc.CodeInvalidRequest, "request too large")
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		jsonrpc.WriteError(w, http.StatusBadRequest, nil, jsonrpc.CodeInvalidRequest, "batch requests are not supported")
		return
	}
	var env jsonrpc.Envelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		jsonrpc.WriteError(w, http.StatusBadRequest, nil, jsonrpc.CodeParse, "parse error")
		return
	}

	reqID := r.Header.Get("X-Request-Id")
	if !validRequestID.MatchString(reqID) {
		reqID = newRequestID() // a caller-supplied id is only passed on if it is plainly an id
	}
	r = r.WithContext(context.WithValue(r.Context(), reqIDKey{}, reqID))
	c := &call{in: r, body: body, env: env, person: person, reqID: reqID, start: time.Now()}

	switch {
	case env.Method == "":
		// A response to a request the server made. This server makes none.
		w.WriteHeader(http.StatusAccepted)
	case !env.IsRequest():
		f.notify(w, c)
	case env.Method == "initialize":
		f.initialize(w, c)
	case env.Method == "ping":
		jsonrpc.WriteResult(w, env.ID, struct{}{})
	default:
		if _, ok := kinds[env.Method]; ok {
			f.list(w, c, env.Method)
			return
		}
		if listMethod, ok := callKinds[env.Method]; ok {
			f.route(w, c, listMethod)
			return
		}
		idx := f.primary(person)
		if idx < 0 {
			jsonrpc.WriteError(w, http.StatusOK, env.ID, jsonrpc.CodeNotConnected, "no backend is connected for this user")
			return
		}
		f.forward(w, c, idx, "")
	}
}

// primary is where a request with no owner of its own goes: the first backend, in configuration order, that
// can serve this person.
func (f *Fuse) primary(person string) int {
	for i := range f.cfg.Backends {
		if f.connected(person, i) {
			return i
		}
	}
	return -1
}

// notify passes a notification on and acknowledges it. The caller's "initialized" is absorbed: mcp-fuse does its
// own handshakes with the backends. Anything else (a cancellation, above all) goes to every backend that has a
// session for this person, because which one owns the request is not known here and a request id that means
// nothing to a backend is ignored by it.
func (f *Fuse) notify(w http.ResponseWriter, c *call) {
	if c.env.Method != "notifications/initialized" {
		ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
		defer cancel()
		var wg sync.WaitGroup
		for i := range f.cfg.Backends {
			if !f.connected(c.person, i) {
				continue
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp, err := f.do(ctx, c.person, i, c.body, false)
				if err != nil {
					if !errors.Is(err, errNoSession) && !errors.Is(err, oauthc.ErrNotConnected) {
						f.log.Warn("notification not delivered", "backend", f.cfg.Backends[i].Name, "method", c.env.Method,
							"request_id", c.reqID, "error", err.Error())
					}
					return
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
			}(i)
		}
		wg.Wait()
	}
	w.WriteHeader(http.StatusAccepted)
}

type initResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	Instructions    string         `json:"instructions"`
}

// initialize opens a session with every backend the person has connected and answers with what they have in
// common: the oldest protocol version any of them speaks, the union of their capabilities, and their
// instructions one after another under the backend's name. A backend the person has not connected yet is
// described in the instructions together with the link that connects it - the one way this server has to say it.
func (f *Fuse) initialize(w http.ResponseWriter, c *call) {
	ctx, cancel := context.WithTimeout(c.in.Context(), f.listTimeout)
	defer cancel()

	type outcome struct {
		env          jsonrpc.Envelope
		err          error
		notConnected bool
	}
	outs := make([]outcome, len(f.cfg.Backends))
	var wg sync.WaitGroup
	for i := range f.cfg.Backends {
		if !f.connected(c.person, i) {
			outs[i].notConnected = true
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := withToken(f, ctx, c.person, i, func(token string) (opened, error) {
				return f.open(ctx, c.person, i, token)
			})
			if err != nil {
				outs[i].err = err
				outs[i].notConnected = errors.Is(err, oauthc.ErrNotConnected)
				return
			}
			outs[i].env = o.env
		}(i)
	}
	wg.Wait()

	var (
		version string
		caps    = map[string]any{}
		parts   []string
		links   []string
		ok      int
		failed  int
	)
	for i, o := range outs {
		bk := f.cfg.Backends[i]
		if o.notConnected {
			links = append(links, "- "+bk.Name+": not connected for you yet. Ask the user to open this link in a browser and sign in "+
				"(valid for 10 minutes), then start the conversation again: "+f.connectURL(c.person, bk.Name))
			continue
		}
		if o.err != nil {
			failed++
			f.log.Warn("initialize failed", "backend", bk.Name, "request_id", c.reqID, "error", o.err.Error())
			continue
		}
		var res initResult
		dec := json.NewDecoder(bytes.NewReader(o.env.Result))
		dec.UseNumber()
		if err := dec.Decode(&res); err != nil {
			failed++
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
	}
	if ok == 0 && failed > 0 {
		jsonrpc.WriteError(w, http.StatusBadGateway, c.env.ID, jsonrpc.CodeBackend, "no backend available")
		return
	}
	if version == "" {
		version = latestSupported
	}
	if len(links) > 0 {
		parts = append(parts, "[mcp-fuse]\n"+strings.Join(links, "\n"))
	}

	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    caps,
		"serverInfo":      map[string]string{"name": "mcp-fuse", "version": f.version},
	}
	if len(parts) > 0 {
		result["instructions"] = strings.Join(parts, "\n\n")
	}
	jsonrpc.WriteResult(w, c.env.ID, result)
	f.log.Info("call", "method", "initialize", "backend", "*", "status", http.StatusOK,
		"ms", time.Since(c.start).Milliseconds(), "request_id", c.reqID)
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
