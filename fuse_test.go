package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend is an MCP server reduced to what these tests need. It records what it was sent.
type fakeBackend struct {
	srv *httptest.Server

	session      string // Mcp-Session-Id issued on initialize ("" = none)
	instructions string
	protocol     string
	caps         string // JSON object
	tools        []string
	callReply    map[string]string // tool name -> exact bytes sent back
	sse          bool              // answer lists as an event stream
	denyAuth     bool              // answer 401 to everything
	staleSession string            // answer 404 to requests that carry this session id
	challenge    string

	mu   sync.Mutex
	seen []seenRequest
}

type seenRequest struct {
	header http.Header
	body   []byte
	method string
}

func newBackend(t *testing.T, b *fakeBackend) *fakeBackend {
	t.Helper()
	if b.protocol == "" {
		b.protocol = "2025-06-18"
	}
	if b.caps == "" {
		b.caps = `{"tools":{}}`
	}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBackend) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var env envelope
	_ = json.Unmarshal(body, &env)
	b.mu.Lock()
	b.seen = append(b.seen, seenRequest{header: r.Header.Clone(), body: body, method: env.Method})
	b.mu.Unlock()

	if b.staleSession != "" && r.Header.Get("Mcp-Session-Id") == b.staleSession {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if b.denyAuth {
		if b.challenge != "" {
			w.Header().Set("WWW-Authenticate", b.challenge)
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
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
		if b.session != "" {
			w.Header().Set("Mcp-Session-Id", b.session)
		}
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
		name := paramString(env.Params, "name")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, b.callReply[name])
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

func (b *fakeBackend) requests(method string) []seenRequest {
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

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// newTestFuse puts a Fuse in front of the given backends (the first stateful one is marked as such) and
// serves it over HTTP.
func newTestFuse(t *testing.T, backends ...*fakeBackend) (*httptest.Server, *Fuse, *logBuffer) {
	t.Helper()
	cfg := Config{
		Resource:             "https://mcp.example.com/mcp",
		AuthorizationServers: []string{"https://auth.example.com"},
	}
	for i, b := range backends {
		cfg.Backends = append(cfg.Backends, Backend{Name: fmt.Sprintf("b%d", i), URL: b.srv.URL, Stateful: b.session != ""})
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	f := newFuse(cfg, slog.New(slog.NewJSONHandler(logs, nil)), "test")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, f, logs
}

func post(t *testing.T, srv *httptest.Server, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
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
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	return names
}

// invariant: a tool call's reply reaches the caller byte for byte, whatever is in it.
func TestToolCallReplyIsNotTouched(t *testing.T) {
	exact := "{ \"jsonrpc\" : \"2.0\",\n\"id\":7,  \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"안녕  \\u00e9 12345678901234567890.10\"}],\"isError\":false} }\n"
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"AskData"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher"}, callReply: map[string]string{"CreateVoucher": exact}})
	srv, _, _ := newTestFuse(t, quvi, exec)

	resp, data := post(t, srv, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"CreateVoucher","arguments":{"n":1}}}`,
		map[string]string{"Authorization": "Bearer abc"})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if sha256.Sum256(data) != sha256.Sum256([]byte(exact)) {
		t.Fatalf("reply was changed:\n got %q\nwant %q", data, exact)
	}
}

// invariant: the arguments reach the backend as the caller wrote them.
func TestToolCallRequestIsNotTouched(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}, callReply: map[string]string{"T": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	srv, _, _ := newTestFuse(t, exec)
	sent := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"T","arguments":{"sql":"SELECT  1","big":12345678901234567890}}}`
	post(t, srv, sent, nil)
	got := exec.requests("tools/call")
	if len(got) != 1 || string(got[0].body) != sent {
		t.Fatalf("backend saw %q", got)
	}
}

func TestInitializeMergesBackends(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", protocol: "2025-06-18", instructions: "ask me",
		caps: `{"tools":{"listChanged":false},"resources":{}}`, tools: []string{"AskData"}})
	exec := newBackend(t, &fakeBackend{protocol: "2025-03-26", instructions: "do things",
		caps: `{"tools":{"listChanged":true},"prompts":{}}`, tools: []string{"CreateVoucher"}})
	srv, _, _ := newTestFuse(t, quvi, exec)

	resp, data := post(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("Mcp-Session-Id"); got != "s-1" {
		t.Errorf("session id = %q, want the stateful backend's", got)
	}
	res := resultOf(t, data)
	var version string
	json.Unmarshal(res["protocolVersion"], &version)
	if version != "2025-03-26" {
		t.Errorf("protocolVersion = %q, want the oldest of the backends", version)
	}
	var caps map[string]map[string]any
	json.Unmarshal(res["capabilities"], &caps)
	for _, want := range []string{"tools", "resources", "prompts"} {
		if _, ok := caps[want]; !ok {
			t.Errorf("capability %q missing from %s", want, res["capabilities"])
		}
	}
	if caps["tools"]["listChanged"] != true {
		t.Errorf("a flag on in one backend must be on: %v", caps["tools"])
	}
	var instr string
	json.Unmarshal(res["instructions"], &instr)
	if !strings.Contains(instr, "[b0]\nask me") || !strings.Contains(instr, "[b1]\ndo things") {
		t.Errorf("instructions = %q", instr)
	}
}

func TestListMergesInConfigurationOrderAndKeepsFirstOnClash(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"AskData", "Shared"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher", "Shared"}})
	srv, _, logs := newTestFuse(t, quvi, exec)

	_, data := post(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	got := strings.Join(toolNames(t, data), ",")
	if got != "AskData,Shared,CreateVoucher" {
		t.Fatalf("tools = %s", got)
	}
	if !strings.Contains(logs.String(), `"dropped":"b1"`) {
		t.Errorf("the clash must be logged: %s", logs.String())
	}
}

func TestListReadsEventStreamReplies(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"Streamed"}, sse: true})
	srv, _, _ := newTestFuse(t, exec)
	_, data := post(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "Streamed" {
		t.Fatalf("tools = %s", got)
	}
}

func TestCredentialAndSessionGoWhereTheyBelong(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"AskData"}, callReply: map[string]string{"AskData": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher"}, callReply: map[string]string{"CreateVoucher": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	srv, _, _ := newTestFuse(t, quvi, exec)
	h := map[string]string{"Authorization": "Bearer abc", "Mcp-Session-Id": "s-1", "X-Tenant": "acme", "MCP-Protocol-Version": "2025-06-18"}

	post(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"AskData"}}`, h)
	post(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"CreateVoucher"}}`, h)

	q := quvi.requests("tools/call")[0].header
	e := exec.requests("tools/call")[0].header
	for name, hdr := range map[string]http.Header{"quvi": q, "exec": e} {
		if hdr.Get("Authorization") != "Bearer abc" {
			t.Errorf("%s: Authorization = %q", name, hdr.Get("Authorization"))
		}
		if hdr.Get("X-Tenant") != "acme" || hdr.Get("Mcp-Protocol-Version") != "2025-06-18" {
			t.Errorf("%s: a header was dropped: %v", name, hdr)
		}
		if hdr.Get("X-Request-Id") == "" || hdr.Get("X-Forwarded-For") == "" {
			t.Errorf("%s: missing X-Request-Id / X-Forwarded-For: %v", name, hdr)
		}
	}
	if q.Get("Mcp-Session-Id") != "s-1" {
		t.Errorf("the stateful backend must get its session, got %q", q.Get("Mcp-Session-Id"))
	}
	if e.Get("Mcp-Session-Id") != "" {
		t.Errorf("a stateless backend must not get the session, got %q", e.Get("Mcp-Session-Id"))
	}
}

func TestHopByHopHeadersStayOnTheirHop(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}, callReply: map[string]string{"T": `{}`}})
	srv, _, _ := newTestFuse(t, exec)
	post(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"T"}}`,
		map[string]string{"Connection": "X-Private", "X-Private": "1", "Keep-Alive": "timeout=5"})
	h := exec.requests("tools/call")[0].header
	if h.Get("X-Private") != "" || h.Get("Keep-Alive") != "" {
		t.Errorf("hop-by-hop headers were forwarded: %v", h)
	}
}

func TestUnknownToolIsAnInvalidParamsError(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}})
	srv, _, _ := newTestFuse(t, exec)
	resp, data := post(t, srv, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"Nope"}}`, nil)
	var env envelope
	json.Unmarshal(data, &env)
	var e rpcError
	json.Unmarshal(env.Error, &e)
	if resp.StatusCode != 200 || e.Code != codeInvalidParams || string(env.ID) != "9" {
		t.Fatalf("status %d, body %s", resp.StatusCode, data)
	}
}

func TestRoutingIsRememberedPerCaller(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}, callReply: map[string]string{"T": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	srv, _, _ := newTestFuse(t, exec)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"T"}}`
	post(t, srv, call, map[string]string{"Authorization": "Bearer a"})
	post(t, srv, call, map[string]string{"Authorization": "Bearer a"})
	if n := len(exec.requests("tools/list")); n != 1 {
		t.Errorf("the second call by the same caller must not list again, lists = %d", n)
	}
	post(t, srv, call, map[string]string{"Authorization": "Bearer b"})
	if n := len(exec.requests("tools/list")); n != 2 {
		t.Errorf("another caller must not share the first one's index, lists = %d", n)
	}
}

func TestOneBackendDownLeavesTheRest(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"AskData"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher"}})
	srv, _, _ := newTestFuse(t, quvi, exec)
	exec.srv.Close()

	_, data := post(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "AskData" {
		t.Fatalf("tools = %s", got)
	}
}

func TestEveryBackendRefusingGives401WithOurChallenge(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", denyAuth: true,
		challenge: `Bearer resource_metadata="https://quvi.internal/.well-known/oauth-protected-resource/mcp/nl2sql", error="invalid_token"`})
	exec := newBackend(t, &fakeBackend{denyAuth: true})
	srv, f, _ := newTestFuse(t, quvi, exec)

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	} {
		resp, _ := post(t, srv, body, nil)
		if resp.StatusCode != 401 {
			t.Fatalf("status %d for %s", resp.StatusCode, body)
		}
		got := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(got, `resource_metadata="`+f.cfg.prmURL+`"`) || strings.Contains(got, "quvi.internal") {
			t.Errorf("challenge = %q", got)
		}
	}
}

func TestChallengeKeepsTheBackendsOtherParameters(t *testing.T) {
	f := &Fuse{cfg: Config{prmURL: "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"}}
	cases := map[string]string{
		"":                             `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`,
		"Bearer":                       `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`,
		`Bearer error="invalid_token"`: `Bearer error="invalid_token", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`,
		`Bearer resource_metadata="https://x/y", scope="a"`: `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="a"`,
	}
	for in, want := range cases {
		if got := f.challenge(in); got != want {
			t.Errorf("challenge(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMetadataAndHealth(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}})
	srv, _, _ := newTestFuse(t, exec)
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if m["resource"] != "https://mcp.example.com/mcp" || fmt.Sprint(m["authorization_servers"]) != "[https://auth.example.com]" {
			t.Errorf("%s -> %v", path, m)
		}
	}
	resp, _ := http.Get(srv.URL + "/healthz")
	if resp.StatusCode != 200 {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
}

func TestMethodsThatAreNotOffered(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}})
	srv, _, _ := newTestFuse(t, exec)
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+"/mcp", nil)
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s -> %d", m, resp.StatusCode)
		}
	}
	if resp, _ := post(t, srv, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil); resp.StatusCode != 400 {
		t.Errorf("batch -> %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv, `not json`, nil); resp.StatusCode != 400 {
		t.Errorf("garbage -> %d", resp.StatusCode)
	}
	resp, data := post(t, srv, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"id":3`) {
		t.Errorf("ping -> %d %s", resp.StatusCode, data)
	}
}

func TestCancellationReachesEveryBackendAndInitializedOnlyTheStatefulOne(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"A"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"B"}})
	srv, _, _ := newTestFuse(t, quvi, exec)

	resp, _ := post(t, srv, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(quvi.requests("notifications/cancelled")) != 1 || len(exec.requests("notifications/cancelled")) != 1 {
		t.Error("a cancellation must reach every backend")
	}
	post(t, srv, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"Mcp-Session-Id": "s-1"})
	if len(quvi.requests("notifications/initialized")) != 1 || len(exec.requests("notifications/initialized")) != 0 {
		t.Error("initialized belongs to the stateful backend only")
	}
}

func TestUnknownMethodsGoToTheStatefulBackend(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"A"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"B"}})
	srv, _, _ := newTestFuse(t, exec, quvi) // the stateful one is not first
	post(t, srv, `{"jsonrpc":"2.0","id":4,"method":"completion/complete","params":{}}`, nil)
	if len(quvi.requests("completion/complete")) != 1 || len(exec.requests("completion/complete")) != 0 {
		t.Error("a method with no owner goes to the stateful backend")
	}
}

func TestBackendWithoutPromptsIsNotAFailure(t *testing.T) {
	exec := newBackend(t, &fakeBackend{tools: []string{"T"}})
	srv, _, _ := newTestFuse(t, exec)
	resp, data := post(t, srv, `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`, nil)
	if resp.StatusCode != 200 || string(resultOf(t, data)["prompts"]) != "[]" {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
}

// invariant: nothing a caller sent or a backend answered is written to the log.
func TestLogsCarryNoBodies(t *testing.T) {
	secret := "SELECT salary FROM payroll WHERE ssn = '900101-1234567'"
	reply := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"홍길동 연봉 99,000,000"}]}}`
	exec := newBackend(t, &fakeBackend{tools: []string{"Query"}, callReply: map[string]string{"Query": reply}})
	srv, _, logs := newTestFuse(t, exec)
	exec.denyAuth = false

	post(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"Query","arguments":{"sql":%q}}}`, secret),
		map[string]string{"Authorization": "Bearer top-secret-token"})
	// The call is logged after its reply has been streamed out, so the log lags the response.
	out := waitForLog(t, logs, `"tool":"Query"`)
	for _, leak := range []string{"payroll", "900101", "홍길동", "99,000,000", "top-secret-token"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q:\n%s", leak, out)
		}
	}
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

// A client of the handshake-less revision must be sent back to `initialize`, not served a partial answer.
func TestNewerRevisionIsRefusedSoClientsFallBack(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", tools: []string{"AskData"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher"}})
	srv, _, _ := newTestFuse(t, quvi, exec)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	resp, data := post(t, srv, list, map[string]string{"MCP-Protocol-Version": "2026-07-28"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var env envelope
	if json.Unmarshal(data, &env) == nil && len(env.Error) > 0 {
		t.Errorf("the refusal must not look like a modern JSON-RPC error, or the client will not fall back: %s", data)
	}
	if len(quvi.requests("tools/list"))+len(exec.requests("tools/list")) != 0 {
		t.Error("a refused request must not reach the backends")
	}

	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", ""} {
		if resp, _ := post(t, srv, list, map[string]string{"MCP-Protocol-Version": v}); resp.StatusCode != http.StatusOK {
			t.Errorf("version %q -> %d", v, resp.StatusCode)
		}
	}
}

// A session the stateful backend has forgotten must surface as 404 so the client initializes again; leaving the
// backend out of the list would silently drop its tools.
func TestExpiredSessionIsAnswered404NotQuietlyDropped(t *testing.T) {
	quvi := newBackend(t, &fakeBackend{session: "s-1", staleSession: "s-old", tools: []string{"AskData"}})
	exec := newBackend(t, &fakeBackend{tools: []string{"CreateVoucher"}, callReply: map[string]string{"CreateVoucher": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	srv, _, _ := newTestFuse(t, quvi, exec)

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"AskData"}}`,
	} {
		resp, _ := post(t, srv, body, map[string]string{"Mcp-Session-Id": "s-old"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404", body, resp.StatusCode)
		}
	}
	// The stateless backend does not care about that session: its own tools keep working.
	if resp, _ := post(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"CreateVoucher"}}`,
		map[string]string{"Mcp-Session-Id": "s-old"}); resp.StatusCode != http.StatusOK {
		t.Errorf("a stateless backend's tool must not be affected, got %d", resp.StatusCode)
	}
	if resp, data := post(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Mcp-Session-Id": "s-1"}); resp.StatusCode != 200 ||
		strings.Join(toolNames(t, data), ",") != "AskData,CreateVoucher" {
		t.Errorf("a live session must still list everything: %d %s", resp.StatusCode, data)
	}
}
