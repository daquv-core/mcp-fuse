package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------------------------
// A fake authorization server. It enforces what the specifications require of a server, so that a client which
// leaves any of it out fails here: PKCE, the redirect URI, the resource (audience), one use of a code.

type fakeAS struct {
	srv *httptest.Server

	mu             sync.Mutex
	n              int
	loginAs        string // who the "browser" is signed in as at this server
	codes          map[string]*codeRec
	tokens         map[string]tokRec
	refreshTokens  map[string]refRec
	front          map[string]frontRec
	expiresIn      int
	rejectRefresh  bool
	refreshCalls   int
	registrations  int
	introspectDown bool
	issParam       bool
	lastToken      string
	confidential   map[string]string // client_id -> secret, for pre-registered confidential clients
	authMethods    []string
	secretInBody   bool // accept client_secret_post instead of Basic
}

type codeRec struct {
	challenge, resource, redirect, clientID, sub string
	used                                         bool
}
type tokRec struct{ aud, sub string }
type refRec struct{ resource, clientID, sub string }
type frontRec struct{ sub, aud string }

func newAS(t *testing.T) *fakeAS {
	t.Helper()
	as := &fakeAS{
		codes: map[string]*codeRec{}, tokens: map[string]tokRec{}, refreshTokens: map[string]refRec{},
		front: map[string]frontRec{}, expiresIn: 3600, issParam: true, loginAs: "alice",
		confidential: map[string]string{"fuse": "s3cret"}, authMethods: []string{"none", "client_secret_basic"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", as.metadata)
	mux.HandleFunc("/register", as.register)
	mux.HandleFunc("/authorize", as.authorize)
	mux.HandleFunc("/token", as.token)
	mux.HandleFunc("/introspect", as.introspect)
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *fakeAS) next(prefix string) string {
	as.n++
	return fmt.Sprintf("%s-%d", prefix, as.n)
}

func (as *fakeAS) metadata(w http.ResponseWriter, r *http.Request) {
	u := as.srv.URL
	writeFrame(w, 200, map[string]any{
		"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token",
		"registration_endpoint": u + "/register", "code_challenge_methods_supported": []string{"S256"},
		"authorization_response_iss_parameter_supported": as.issParam,
		"token_endpoint_auth_methods_supported":          as.authMethods,
	})
}

func (as *fakeAS) register(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	defer as.mu.Unlock()
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.RedirectURIs) == 0 || req.AuthMethod != "none" {
		http.Error(w, "bad registration", 400)
		return
	}
	as.registrations++
	writeFrame(w, 201, map[string]string{"client_id": as.next("client")})
}

func (as *fakeAS) authorize(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	defer as.mu.Unlock()
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("redirect_uri") == "" || q.Get("resource") == "" || q.Get("client_id") == "" || q.Get("state") == "" {
		http.Error(w, "invalid authorization request", 400)
		return
	}
	code := as.next("code")
	as.codes[code] = &codeRec{challenge: q.Get("code_challenge"), resource: q.Get("resource"), redirect: q.Get("redirect_uri"),
		clientID: q.Get("client_id"), sub: as.loginAs}
	u, _ := url.Parse(q.Get("redirect_uri"))
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	if as.issParam {
		rq.Set("iss", as.srv.URL)
	}
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (as *fakeAS) token(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	defer as.mu.Unlock()
	_ = r.ParseForm()
	fail := func(code string) { writeFrame(w, 400, map[string]string{"error": code}) }
	if secret, ok := as.confidential[r.PostForm.Get("client_id")]; ok {
		got := r.PostForm.Get("client_secret")
		if !as.secretInBody {
			user, pass, hasBasic := r.BasicAuth()
			got = pass
			if !hasBasic || user != r.PostForm.Get("client_id") {
				got = ""
			}
		}
		if got != secret {
			writeFrame(w, 401, map[string]string{"error": "invalid_client"})
			return
		}
	}
	issue := func(sub, resource, clientID string) {
		at, rt := as.next("at"), as.next("rt")
		as.tokens[at] = tokRec{aud: resource, sub: sub}
		as.refreshTokens[rt] = refRec{resource: resource, clientID: clientID, sub: sub}
		as.lastToken = at
		writeFrame(w, 200, map[string]any{"access_token": at, "token_type": "Bearer", "refresh_token": rt, "expires_in": as.expiresIn})
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		rec := as.codes[r.PostForm.Get("code")]
		if rec == nil || rec.used {
			fail("invalid_grant")
			return
		}
		rec.used = true
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if b64(sum[:]) != rec.challenge || r.PostForm.Get("redirect_uri") != rec.redirect ||
			r.PostForm.Get("client_id") != rec.clientID || r.PostForm.Get("resource") != rec.resource {
			fail("invalid_grant")
			return
		}
		issue(rec.sub, rec.resource, rec.clientID)
	case "refresh_token":
		as.refreshCalls++
		rec, ok := as.refreshTokens[r.PostForm.Get("refresh_token")]
		if !ok || as.rejectRefresh || rec.clientID != r.PostForm.Get("client_id") || rec.resource != r.PostForm.Get("resource") {
			fail("invalid_grant")
			return
		}
		delete(as.refreshTokens, r.PostForm.Get("refresh_token")) // rotation: a refresh token is good once
		issue(rec.sub, rec.resource, rec.clientID)
	default:
		fail("unsupported_grant_type")
	}
}

func (as *fakeAS) introspect(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.introspectDown {
		http.Error(w, "down", 500)
		return
	}
	if user, pass, ok := r.BasicAuth(); !ok || user != "fuse" || pass != "s3cret" {
		http.Error(w, "unauthorized", 401)
		return
	}
	_ = r.ParseForm()
	token := r.PostForm.Get("token")
	rec, ok := as.front[token]
	if t, issued := as.tokens[token]; !ok && issued {
		rec, ok = frontRec{sub: t.sub, aud: t.aud}, true
	}
	if !ok {
		writeFrame(w, 200, map[string]any{"active": false})
		return
	}
	writeFrame(w, 200, map[string]any{"active": true, "sub": rec.sub, "iss": as.srv.URL, "aud": rec.aud, "exp": time.Now().Add(10 * time.Minute).Unix()})
}

func (as *fakeAS) setLogin(sub string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.loginAs = sub
}

func (as *fakeAS) snapshot() (refreshCalls, registrations int, last string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.refreshCalls, as.registrations, as.lastToken
}

// ---------------------------------------------------------------------------------------------------------------
// A fake MCP backend. With `as` set it is a standard resource server: it accepts only a token issued for it.

type fakeMCP struct {
	srv *httptest.Server
	as  *fakeAS

	sessions     bool // issues Mcp-Session-Id
	instructions string
	protocol     string
	caps         string
	tools        []string
	callReply    map[string]string // tool name -> exact bytes sent back
	sse          bool              // answer lists as an event stream

	mu     sync.Mutex
	seen   []seenRequest
	live   map[string]bool
	sessN  int
	inits  int
	revoke bool // answer 401 to every request, whatever the token
}

type seenRequest struct {
	header http.Header
	body   []byte
	method string
}

func newMCP(t *testing.T, b *fakeMCP) *fakeMCP {
	t.Helper()
	if b.protocol == "" {
		b.protocol = "2025-06-18"
	}
	if b.caps == "" {
		b.caps = `{"tools":{}}`
	}
	b.live = map[string]bool{}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeMCP) url() string { return b.srv.URL + "/mcp" }

func (b *fakeMCP) metadataURL() string {
	return b.srv.URL + "/.well-known/oauth-protected-resource/mcp"
}

func (b *fakeMCP) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" && b.as != nil {
		writeFrame(w, 200, map[string]any{"resource": b.url(), "authorization_servers": []string{b.as.srv.URL}})
		return
	}
	body, _ := io.ReadAll(r.Body)
	var env envelope
	_ = json.Unmarshal(body, &env)

	if b.as != nil {
		b.as.mu.Lock()
		tok, ok := b.as.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		b.as.mu.Unlock()
		if !ok || tok.aud != b.url() || b.revoke {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+b.metadataURL()+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	b.mu.Lock()
	b.seen = append(b.seen, seenRequest{header: r.Header.Clone(), body: body, method: env.Method})
	b.mu.Unlock()

	if b.sessions && env.Method != "initialize" {
		b.mu.Lock()
		known := b.live[r.Header.Get("Mcp-Session-Id")]
		b.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	reply := func(result string) {
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, env.ID, result)
		if b.sse && env.Method != "initialize" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", frame)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, frame)
	}
	switch env.Method {
	case "initialize":
		b.mu.Lock()
		b.inits++
		if b.sessions {
			b.sessN++
			id := fmt.Sprintf("sess-%d", b.sessN)
			b.live[id] = true
			w.Header().Set("Mcp-Session-Id", id)
		}
		b.mu.Unlock()
		ins, _ := json.Marshal(b.instructions)
		reply(fmt.Sprintf(`{"protocolVersion":%q,"capabilities":%s,"serverInfo":{"name":"fake","version":"1"},"instructions":%s}`,
			b.protocol, b.caps, ins))
	case "tools/list":
		var items []string
		for _, name := range b.tools {
			items = append(items, fmt.Sprintf(`{"name":%q,"description":"d","inputSchema":{"type":"object"}}`, name))
		}
		reply(`{"tools":[` + strings.Join(items, ",") + `]}`)
	case "tools/call":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, b.callReply[paramString(env.Params, "name")])
	case "prompts/list":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, env.ID)
	default:
		if env.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		reply(`{}`)
	}
}

func (b *fakeMCP) requests(method string) []seenRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []seenRequest
	for _, s := range b.seen {
		if s.method == method {
			out = append(out, s)
		}
	}
	return out
}

func (b *fakeMCP) allRequests() []seenRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]seenRequest(nil), b.seen...)
}

func (b *fakeMCP) forgetSessions() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live = map[string]bool{}
}

// ---------------------------------------------------------------------------------------------------------------

type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func waitForLog(t *testing.T, logs *logBuffer, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if out := logs.String(); strings.Contains(out, want) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line containing %s:\n%s", want, logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// world is mcp-fuse in front of the given backends, with a fake authorization server behind everything.
type world struct {
	t         *testing.T
	as        *fakeAS
	backends  []*fakeMCP
	srv       *httptest.Server
	f         *Fuse
	logs      *logBuffer
	secret    []byte
	statePath string
}

const (
	aliceToken = "front-alice"
	bobToken   = "front-bob"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef-test")

// newWorld wires the backends to a fake authorization server (those with auth "none" are listed in `open`),
// starts mcp-fuse in front of them, and registers two callers, alice and bob.
func newWorld(t *testing.T, open map[int]bool, backends ...*fakeMCP) *world {
	t.Helper()
	return newWorldWith(t, open, nil, backends...)
}

// newWorldWith is newWorld with a hook that may change the configuration, the dependencies and the fake
// authorization server before mcp-fuse starts.
func newWorldWith(t *testing.T, open map[int]bool, tweak func(*Config, *deps, *fakeAS), backends ...*fakeMCP) *world {
	t.Helper()
	as := newAS(t)
	w := &world{t: t, as: as, backends: backends, logs: &logBuffer{}, secret: testSecret}

	var f *Fuse
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { f.ServeHTTP(rw, r) }))
	resource := "http://" + srv.Listener.Addr().String() + "/mcp"

	cfg := Config{Resource: resource, AuthorizationServers: []string{as.srv.URL}}
	for i, b := range backends {
		bk := Backend{Name: fmt.Sprintf("b%d", i), URL: b.url(), Auth: "oauth"}
		if open[i] {
			bk.Auth = "none"
		} else {
			b.as = as
		}
		cfg.Backends = append(cfg.Backends, bk)
	}
	w.statePath = filepath.Join(t.TempDir(), "state.bin")
	cfg.StateFile, cfg.SecretFile = w.statePath, "unused-in-tests"
	cfg.Auth = Auth{IntrospectionURL: as.srv.URL + "/introspect", ClientID: "fuse"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}

	st, err := openStore(w.statePath, w.secret)
	if err != nil {
		t.Fatal(err)
	}
	d := deps{store: st, secret: w.secret, auth: newIntrospector(as.srv.URL+"/introspect", "fuse", "s3cret", resource),
		clientSecrets: map[string]string{}, frontSecret: "s3cret"}
	if tweak != nil {
		tweak(&cfg, &d, as)
		if err := cfg.validate(); err != nil {
			t.Fatal(err)
		}
	}
	f = newFuse(cfg, slog.New(slog.NewJSONHandler(w.logs, nil)), "test", d)
	srv.Start()
	t.Cleanup(srv.Close)
	w.srv, w.f = srv, f

	as.front[aliceToken] = frontRec{sub: "alice", aud: resource}
	as.front[bobToken] = frontRec{sub: "bob", aud: resource}
	return w
}

// newBrowser is a client that keeps cookies and follows redirects.
func newBrowser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// linkFor reads the connect link for a backend out of what the assistant was told at initialize.
func (w *world) linkFor(token, backend string) string {
	w.t.Helper()
	_, data := w.post(token, initBody, nil)
	m := regexp.MustCompile(`http://\S+/connect/` + backend + `\?t=\S+`).FindString(instructionsOf(w.t, data))
	if m == "" {
		w.t.Fatalf("no connect link for %s in %s", backend, data)
	}
	return m
}

func (w *world) post(token, body string, headers map[string]string) (*http.Response, []byte) {
	w.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, w.srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`

func instructionsOf(t *testing.T, data []byte) string {
	t.Helper()
	var s string
	_ = json.Unmarshal(resultOf(t, data)["instructions"], &s)
	return s
}

// connect plays the person: reads the link out of what the assistant was told, opens it in a "browser" that is
// signed in as `person` at the authorization server, and follows the redirects back.
func (w *world) connect(person, token, backend string) {
	w.t.Helper()
	w.as.setLogin(person)
	_, data := w.post(token, initBody, nil)
	m := regexp.MustCompile(`http://\S+/connect/` + backend + `\?t=\S+`).FindString(instructionsOf(w.t, data))
	if m == "" {
		w.t.Fatalf("no connect link for %s in %s", backend, data)
	}
	resp, err := newBrowser().Get(m)
	if err != nil {
		w.t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Connected") {
		w.t.Fatalf("connecting %s: %d %s", backend, resp.StatusCode, page)
	}
}

func resultOf(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("not JSON: %s", data)
	}
	if len(env.Result) == 0 {
		t.Fatalf("no result in %s", data)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func toolNames(t *testing.T, data []byte) []string {
	t.Helper()
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resultOf(t, data)["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	return names
}

func tmpFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
