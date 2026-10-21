package fuse

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/daquv-core/mcp-fuse/internal/config"
	"github.com/daquv-core/mcp-fuse/internal/jsonrpc"
	"github.com/daquv-core/mcp-fuse/internal/state"
)

const listBody = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

func callBody(id int, tool string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool}})
	return string(b)
}

// ---- who is calling ----------------------------------------------------------------------------------------------

func TestCallersMustPresentATokenIssuedForThisServer(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	w.as.front["other-resource"] = frontRec{sub: "mallory", aud: "https://somewhere.else/mcp"}

	for name, tc := range map[string]struct {
		token       string
		wantInvalid bool
	}{
		"no token":       {"", false},
		"unknown token":  {"nope", true},
		"wrong audience": {"other-resource", true},
	} {
		resp, _ := w.post(tc.token, listBody, nil)
		got := resp.Header.Get("WWW-Authenticate")
		if resp.StatusCode != 401 || !strings.Contains(got, `resource_metadata="`+w.f.cfg.PRMURL+`"`) {
			t.Errorf("%s: %d, challenge %q", name, resp.StatusCode, got)
		}
		if strings.Contains(got, "invalid_token") != tc.wantInvalid {
			t.Errorf("%s: challenge %q", name, got)
		}
	}

	w.as.mu.Lock()
	w.as.introspectDown = true
	w.as.mu.Unlock()
	if resp, _ := w.post("never-cached", listBody, nil); resp.StatusCode != 503 {
		t.Errorf("introspection down -> %d, want 503 (not a 401 that sends the client to log in again)", resp.StatusCode)
	}
}

// ---- connecting a backend ----------------------------------------------------------------------------------------

func TestInitializeSaysHowToConnectWhatIsNotConnected(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"A"}}), newMCP(t, &fakeMCP{tools: []string{"B"}}))
	resp, data := w.post(aliceToken, initBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if resp.Header.Get("Mcp-Session-Id") != "" {
		t.Error("callers are given no session; mcp-fuse keeps its own with each backend")
	}
	ins := instructionsOf(t, data)
	for _, name := range []string{"b0", "b1"} {
		if !regexp.MustCompile(`http://\S+/mcp/connect/` + name + `\?t=\S+`).MatchString(ins) {
			t.Errorf("no link for %s in %q", name, ins)
		}
	}
	_, data = w.post(aliceToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 0 {
		t.Errorf("nothing is connected yet, tools = %v", got)
	}
}

// invariant: a backend never sees the token the caller presented, only its own, issued for it.
func TestEachBackendGetsItsOwnTokenAndNothingElse(t *testing.T) {
	sessionful := newMCP(t, &fakeMCP{sessions: true, tools: []string{"Search"}, callReply: map[string]string{"Search": `{"jsonrpc":"2.0","id":5,"result":{}}`}})
	plain := newMCP(t, &fakeMCP{tools: []string{"CreateOrder"}, callReply: map[string]string{"CreateOrder": `{"jsonrpc":"2.0","id":6,"result":{}}`}})
	w := newWorld(t, nil, sessionful, plain)
	w.connect("alice", aliceToken, "b0")
	w.connect("alice", aliceToken, "b1")

	w.post(aliceToken, initBody, map[string]string{"Cookie": "a=b", "X-Tenant": "acme", "X-Forwarded-For": "9.9.9.9", "Mcp-Session-Id": "client-made-this"})
	w.post(aliceToken, listBody, nil)
	w.post(aliceToken, callBody(5, "Search"), nil)
	w.post(aliceToken, callBody(6, "CreateOrder"), nil)

	allowed := map[string]bool{"Accept": true, "Accept-Encoding": true, "Authorization": true, "Content-Length": true,
		"Content-Type": true, "Mcp-Protocol-Version": true, "Mcp-Session-Id": true, "User-Agent": true, "X-Request-Id": true}
	for name, b := range map[string]*fakeMCP{"orders": sessionful, "plain": plain} {
		seen := b.allRequests()
		if len(seen) == 0 {
			t.Fatalf("%s saw nothing", name)
		}
		for _, s := range seen {
			if strings.Contains(string(s.body), aliceToken) {
				t.Errorf("%s: the caller's token is in a body", name)
			}
			for k, vs := range s.header {
				if !allowed[k] {
					t.Errorf("%s: header %s=%v is not one an MCP client sends", name, k, vs)
				}
				for _, v := range vs {
					if strings.Contains(v, aliceToken) || v == "client-made-this" {
						t.Errorf("%s: %s carries something the caller supplied: %q", name, k, v)
					}
				}
			}
			tok := strings.TrimPrefix(s.header.Get("Authorization"), "Bearer ")
			w.as.mu.Lock()
			rec := w.as.tokens[tok]
			w.as.mu.Unlock()
			if rec.aud != b.url() || rec.sub != "alice" {
				t.Errorf("%s: token %q is for %+v, want audience %s and alice", name, tok, rec, b.url())
			}
		}
	}
	qt := sessionful.requests("tools/call")[0].header.Get("Authorization")
	et := plain.requests("tools/call")[0].header.Get("Authorization")
	if qt == et {
		t.Error("both backends were sent the same token")
	}
}

// stopAtBackend is a browser that follows redirects but stops at the authorization request to the backend's
// server, so that a test can look at it, or hand it to someone else.
func (w *world) stopAtBackend() *http.Client {
	c := newBrowser()
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if req.URL.Path == "/authorize" && req.URL.Query().Get("resource") != w.f.cfg.Resource {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return c
}

func TestConnectBeginsWithProofOfWhoIsConnecting(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}), newMCP(t, &fakeMCP{tools: []string{"U"}}))
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(c *http.Client, u string) int {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	good := w.linkFor(aliceToken, "b0")
	if status(noRedirect, strings.Replace(good, "?t=", "?t=x", 1)) != 400 {
		t.Error("a tampered ticket was accepted")
	}
	if status(noRedirect, strings.Replace(w.linkFor(aliceToken, "b1"), "/connect/b1", "/connect/b0", 1)) != 400 {
		t.Error("a ticket for b1 opened b0")
	}
	if status(noRedirect, w.srv.URL+"/mcp/connect/b0") != 400 {
		t.Error("no ticket was accepted")
	}
	identify := w.srv.URL + "/mcp/connect/identify"
	if status(noRedirect, identify+"?code=c&state=forged") != 400 {
		t.Error("a forged state was accepted")
	}

	// The first stop is the sign-in at the callers' authorization server, not the backend's.
	browser := &http.Client{Jar: newBrowser().Jar, CheckRedirect: noRedirect.CheckRedirect}
	redir, err := browser.Get(good)
	if err != nil || redir.StatusCode != 302 {
		t.Fatalf("start: %v %v", redir, err)
	}
	authURL, _ := url.Parse(redir.Header.Get("Location"))
	q := authURL.Query()
	if !strings.HasPrefix(redir.Header.Get("Location"), w.as.srv.URL+"/authorize") || q.Get("client_id") != "fuse" ||
		q.Get("resource") != w.f.cfg.Resource || q.Get("redirect_uri") != identify || q.Get("code_challenge_method") != "S256" {
		t.Errorf("sign-in request: %v", redir.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range redir.Cookies() {
		if c.Name == cookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/mcp/connect" {
		t.Errorf("the flow must be pinned to the browser with a cookie, got %+v", cookie)
	}
	state := q.Get("state")

	// From here on every request carries the cookie, so a refusal can only be for the reason being tested.
	// The sign-in state is not a backend state: it cannot be used to skip the proof of who is connecting.
	if status(browser, w.srv.URL+"/mcp/connect/callback?code=x&state="+state+"&iss="+url.QueryEscape(w.as.srv.URL)) != 400 {
		t.Error("the sign-in state was accepted at the backend step")
	}
	// Each of these consumes a state, so each starts a fresh flow in the same browser.
	fresh := func() string {
		r, err := browser.Get(w.linkFor(aliceToken, "b0"))
		if err != nil || r.StatusCode != 302 {
			t.Fatalf("start: %v %v", r, err)
		}
		r.Body.Close()
		u, _ := url.Parse(r.Header.Get("Location"))
		return u.Query().Get("state")
	}
	if status(browser, identify+"?code=x&state="+fresh()+"&iss=https://evil.example") != 400 {
		t.Error("a sign-in response from the wrong issuer was accepted")
	}
	st := fresh()
	if status(browser, identify+"?code=x&state="+st+"&iss="+url.QueryEscape(w.as.srv.URL)) == 400 {
		t.Error("a well-formed sign-in response was refused for the wrong reason")
	}
	if status(browser, identify+"?code=x&state="+st+"&iss="+url.QueryEscape(w.as.srv.URL)) != 400 {
		t.Error("a state was usable twice")
	}
	// Without the cookie of the browser that began the flow, nothing is accepted, whatever else is right.
	if status(noRedirect, identify+"?code=x&state="+fresh()+"&iss="+url.QueryEscape(w.as.srv.URL)) != 400 {
		t.Error("a sign-in response from another browser was accepted")
	}
	if w.f.connected(mustPerson(t, w, "alice"), 0) {
		t.Error("nothing should have been connected by any of that")
	}
}

// The ticket says whom a link was made for; it proves nothing about who opens it. A link forwarded to someone else
// must not store that person's token under the first person's name.
func TestALinkOpenedByAnotherPersonConnectsNothing(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, plain)
	link := w.linkFor(aliceToken, "b0") // made for alice

	w.as.setLogin("bob") // ... but bob is the one signed in at the browser that opens it
	resp, err := newBrowser().Get(link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
	for _, who := range []string{"alice", "bob"} {
		if w.f.connected(mustPerson(t, w, who), 0) {
			t.Errorf("%s ended up with a connection", who)
		}
	}
	if len(plain.allRequests()) != 0 {
		t.Error("the backend was contacted for a link its holder was not entitled to")
	}
	if !strings.Contains(w.logs.String(), "not the one the link was made for") {
		t.Error("the refusal should be logged")
	}
}

// Proving who is connecting at the start is not enough: the backend's sign-in URL can be handed to someone else.
// The flow has to be finished in the browser it began in.
func TestAFlowIsFinishedOnlyInTheBrowserItBegan(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, plain)
	link := w.linkFor(aliceToken, "b0")

	// alice (really alice) opens the link and gets as far as the backend's sign-in, then passes that URL on.
	w.as.setLogin("alice")
	resp, err := w.stopAtBackend().Get(link)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("expected to stop at the backend sign-in: %v %v", resp, err)
	}
	backendSignIn := resp.Header.Get("Location")
	resp.Body.Close()

	// bob opens it in his own browser, signs in as bob, and is sent back to mcp-fuse.
	w.as.setLogin("bob")
	resp, err = newBrowser().Get(backendSignIn)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("finishing in another browser -> %d, want 400", resp.StatusCode)
	}
	for _, who := range []string{"alice", "bob"} {
		if w.f.connected(mustPerson(t, w, who), 0) {
			t.Errorf("%s ended up with a connection", who)
		}
	}
	if !strings.Contains(w.logs.String(), "different browser") {
		t.Error("the refusal should be logged")
	}
}

func TestBackendStepAsksForWhatItShould(t *testing.T) {
	b := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, b)
	w.as.setLogin("alice")
	resp, err := w.stopAtBackend().Get(w.linkFor(aliceToken, "b0"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("%v %v", resp, err)
	}
	resp.Body.Close()
	q, _ := url.Parse(resp.Header.Get("Location"))
	if q.Query().Get("code_challenge_method") != "S256" || q.Query().Get("resource") != b.url() ||
		q.Query().Get("redirect_uri") != w.srv.URL+"/mcp/connect/callback" {
		t.Errorf("backend authorization request: %v", resp.Header.Get("Location"))
	}
}

func TestOneRegistrationPerAuthorizationServerAndBackend(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	w.connect("alice", aliceToken, "b0")
	w.connect("bob", bobToken, "b0")
	if _, regs, _ := w.as.snapshot(); regs != 1 {
		t.Errorf("registrations = %d, want 1 (remembered across people)", regs)
	}
}

// ---- serving ------------------------------------------------------------------------------------------------------

func TestMergedViewAfterConnecting(t *testing.T) {
	sessionful := newMCP(t, &fakeMCP{sessions: true, protocol: "2025-06-18", instructions: "ask me", caps: `{"tools":{"listChanged":false},"resources":{}}`, tools: []string{"Search", "Shared"}})
	plain := newMCP(t, &fakeMCP{protocol: "2025-03-26", instructions: "do things", caps: `{"tools":{"listChanged":true},"prompts":{}}`, tools: []string{"CreateOrder", "Shared"}})
	w := newWorld(t, nil, sessionful, plain)
	w.connect("alice", aliceToken, "b0")
	w.connect("alice", aliceToken, "b1")

	resp, data := w.post(aliceToken, initBody, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, data)
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
	ins := instructionsOf(t, data)
	if !strings.Contains(ins, "[b0]\nask me") || !strings.Contains(ins, "[b1]\ndo things") || strings.Contains(ins, "/connect/") {
		t.Errorf("instructions = %q", ins)
	}

	_, data = w.post(aliceToken, listBody, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "Search,Shared,CreateOrder" {
		t.Fatalf("tools = %s (the earlier backend keeps a shared name)", got)
	}
	if !strings.Contains(w.logs.String(), `"dropped":"b1"`) {
		t.Error("the clash must be logged")
	}
}

// invariant: a tool call's request and reply pass through byte for byte, whatever is in them.
func TestToolCallsPassThroughUntouched(t *testing.T) {
	exact := "{ \"jsonrpc\" : \"2.0\",\n\"id\":7,  \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"안녕  \\u00e9 12345678901234567890.10\"}],\"isError\":false} }\n"
	plain := newMCP(t, &fakeMCP{tools: []string{"CreateOrder"}, callReply: map[string]string{"CreateOrder": exact}})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")

	sent := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"CreateOrder","arguments":{"sql":"SELECT  1","big":12345678901234567890}}}`
	resp, data := w.post(aliceToken, sent, nil)
	if resp.StatusCode != 200 || sha256.Sum256(data) != sha256.Sum256([]byte(exact)) {
		t.Fatalf("reply was changed:\n got %q\nwant %q", data, exact)
	}
	got := plain.requests("tools/call")
	if len(got) != 1 || string(got[0].body) != sent {
		t.Fatalf("backend saw %q", got)
	}
}

func TestEachPersonSeesOnlyWhatTheyConnected(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"CreateOrder"}, callReply: map[string]string{"CreateOrder": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")

	_, data := w.post(aliceToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 1 {
		t.Fatalf("alice: %v", got)
	}
	_, data = w.post(bobToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 0 {
		t.Errorf("bob connected nothing but sees %v", got)
	}
	_, data = w.post(bobToken, callBody(1, "CreateOrder"), nil)
	if !strings.Contains(string(data), "unknown tool") {
		t.Errorf("bob can call alice's tool: %s", data)
	}
	w.connect("bob", bobToken, "b0")
	_, data = w.post(bobToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 1 {
		t.Errorf("bob after connecting: %v", got)
	}
	subs := map[string]bool{}
	for _, s := range plain.allRequests() {
		w.as.mu.Lock()
		subs[w.as.tokens[strings.TrimPrefix(s.header.Get("Authorization"), "Bearer ")].sub] = true
		w.as.mu.Unlock()
	}
	if !subs["alice"] || !subs["bob"] || len(subs) != 2 {
		t.Errorf("tokens used at the backend belonged to %v", subs)
	}
}

func TestExpiredTokensAreRefreshedAndARevokedGrantAsksToConnectAgain(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}, callReply: map[string]string{"T": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	w := newWorld(t, nil, plain)
	w.as.mu.Lock()
	w.as.expiresIn = 10 // inside the refresh margin: every use must refresh
	w.as.mu.Unlock()
	w.connect("alice", aliceToken, "b0")

	before, _, _ := w.as.snapshot()
	for i := 0; i < 2; i++ {
		if resp, data := w.post(aliceToken, callBody(1, "T"), nil); resp.StatusCode != 200 || !strings.Contains(string(data), `"result"`) {
			t.Fatalf("call %d: %d %s", i, resp.StatusCode, data)
		}
	}
	after, _, _ := w.as.snapshot()
	if after <= before {
		t.Error("an expiring token was used without refreshing it")
	}
	tokens := map[string]bool{}
	for _, s := range plain.requests("tools/call") {
		tokens[s.header.Get("Authorization")] = true
	}
	if len(tokens) < 2 {
		t.Errorf("the backend should have seen refreshed tokens, saw %v", tokens)
	}

	w.as.mu.Lock()
	w.as.rejectRefresh = true
	w.as.mu.Unlock()
	_, data := w.post(aliceToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 0 {
		t.Errorf("tools after revocation = %v", got)
	}
	if w.f.connected(mustPerson(t, w, "alice"), 0) {
		t.Error("the grant should be gone once the authorization server refuses its refresh token")
	}
	_, data = w.post(aliceToken, initBody, nil)
	if !strings.Contains(instructionsOf(t, data), "/connect/b0?t=") {
		t.Error("a revoked grant must be offered for connecting again")
	}
}

func TestBackendThatRefusesTheTokenDropsTheGrant(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")
	person := mustPerson(t, w, "alice")
	if !w.f.connected(person, 0) {
		t.Fatal("not connected after connecting")
	}
	plain.mu.Lock()
	plain.revoke = true
	plain.mu.Unlock()

	_, data := w.post(aliceToken, listBody, nil)
	if got := toolNames(t, data); len(got) != 0 {
		t.Errorf("tools = %v", got)
	}
	if w.f.connected(person, 0) {
		t.Error("the grant should have been dropped after the backend refused a fresh token too")
	}
}

// ---- sessions are between mcp-fuse and the backend ---------------------------------------------------------------

func TestSessionsAreOpenedAndRestoredByMcpFuse(t *testing.T) {
	sessionful := newMCP(t, &fakeMCP{sessions: true, tools: []string{"Search"}, callReply: map[string]string{"Search": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	w := newWorld(t, nil, sessionful)
	w.connect("alice", aliceToken, "b0")

	w.post(aliceToken, initBody, nil)
	resp, data := w.post(aliceToken, callBody(1, "Search"), map[string]string{"Mcp-Session-Id": "client-made-this"})
	if resp.StatusCode != 200 || resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, data)
	}
	for _, s := range sessionful.requests("tools/call") {
		if id := s.header.Get("Mcp-Session-Id"); id == "" || id == "client-made-this" {
			t.Errorf("backend session header = %q, want mcp-fuse's own", id)
		}
		if s.header.Get("Mcp-Protocol-Version") != "2025-06-18" {
			t.Errorf("protocol version header = %q, want the one the backend agreed to", s.header.Get("Mcp-Protocol-Version"))
		}
	}

	inits := len(sessionful.requests("initialize"))
	sessionful.forgetSessions()
	resp, data = w.post(aliceToken, callBody(1, "Search"), nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"result"`) {
		t.Fatalf("after the backend lost its sessions: %d %s", resp.StatusCode, data)
	}
	if len(sessionful.requests("initialize")) != inits+1 {
		t.Error("a fresh session should have been opened once")
	}
}

// ---- what is not served ------------------------------------------------------------------------------------------

func TestNewerRevisionIsRefusedSoClientsFallBack(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")
	n := len(plain.allRequests())

	resp, data := w.post(aliceToken, listBody, map[string]string{"MCP-Protocol-Version": "2026-07-28"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var env jsonrpc.Envelope
	if json.Unmarshal(data, &env) == nil && len(env.Error) > 0 {
		t.Errorf("the refusal must not look like a modern JSON-RPC error, or the client will not fall back: %s", data)
	}
	if len(plain.allRequests()) != n {
		t.Error("a refused request must not reach the backends")
	}
	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", ""} {
		if resp, _ := w.post(aliceToken, listBody, map[string]string{"MCP-Protocol-Version": v}); resp.StatusCode != http.StatusOK {
			t.Errorf("version %q -> %d", v, resp.StatusCode)
		}
	}
}

func TestMethodsThatAreNotOffered(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(m, w.srv.URL+"/mcp", nil)
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s -> %d", m, resp.StatusCode)
		}
	}
	if resp, _ := w.post(aliceToken, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil); resp.StatusCode != 400 {
		t.Errorf("batch -> %d", resp.StatusCode)
	}
	if resp, _ := w.post(aliceToken, `not json`, nil); resp.StatusCode != 400 {
		t.Errorf("garbage -> %d", resp.StatusCode)
	}
	resp, data := w.post(aliceToken, `{"jsonrpc":"2.0","id":3,"method":"ping"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"id":3`) {
		t.Errorf("ping -> %d %s", resp.StatusCode, data)
	}
}

func TestMetadataAndHealth(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(w.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if m["resource"] != w.f.cfg.Resource {
			t.Errorf("%s -> %v", path, m)
		}
	}
	if resp, _ := http.Get(w.srv.URL + "/healthz"); resp.StatusCode != 200 {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
}

// ---- merging details ---------------------------------------------------------------------------------------------

func TestListReadsEventStreamRepliesAndToleratesAMissingPromptsMethod(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"Streamed"}, sse: true})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")
	_, data := w.post(aliceToken, listBody, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "Streamed" {
		t.Fatalf("tools = %s", got)
	}
	resp, data := w.post(aliceToken, `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`, nil)
	if resp.StatusCode != 200 || string(resultOf(t, data)["prompts"]) != "[]" {
		t.Fatalf("prompts: %d %s", resp.StatusCode, data)
	}
}

func TestOneBackendDownLeavesTheRest(t *testing.T) {
	a := newMCP(t, &fakeMCP{tools: []string{"A"}})
	b := newMCP(t, &fakeMCP{tools: []string{"B"}})
	w := newWorld(t, nil, a, b)
	w.connect("alice", aliceToken, "b0")
	w.connect("alice", aliceToken, "b1")
	b.srv.Close()

	_, data := w.post(aliceToken, listBody, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "A" {
		t.Fatalf("tools = %s", got)
	}
}

func TestCancellationReachesBackendsThatHaveASession(t *testing.T) {
	sessionful := newMCP(t, &fakeMCP{sessions: true, tools: []string{"A"}})
	plain := newMCP(t, &fakeMCP{tools: []string{"B"}})
	w := newWorld(t, nil, sessionful, plain)
	w.connect("alice", aliceToken, "b0")
	w.connect("alice", aliceToken, "b1")
	w.post(aliceToken, initBody, nil)

	resp, _ := w.post(aliceToken, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(sessionful.requests("notifications/cancelled")) != 1 || len(plain.requests("notifications/cancelled")) != 1 {
		t.Error("a cancellation must reach every backend that has a session")
	}
	n := len(sessionful.requests("notifications/initialized"))
	w.post(aliceToken, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if len(sessionful.requests("notifications/initialized")) != n {
		t.Error("the caller's initialized notification was passed on")
	}
}

func TestUnknownMethodsGoToTheFirstBackendTheCallerCanUse(t *testing.T) {
	a := newMCP(t, &fakeMCP{tools: []string{"A"}})
	b := newMCP(t, &fakeMCP{tools: []string{"B"}})
	w := newWorld(t, nil, a, b)
	w.connect("alice", aliceToken, "b1") // only the second is connected
	w.post(aliceToken, `{"jsonrpc":"2.0","id":4,"method":"completion/complete","params":{}}`, nil)
	if len(b.requests("completion/complete")) != 1 || len(a.requests("completion/complete")) != 0 {
		t.Error("a method with no owner goes to the first backend the caller can use")
	}
	_, data := w.post(bobToken, `{"jsonrpc":"2.0","id":4,"method":"completion/complete","params":{}}`, nil)
	if !strings.Contains(string(data), "no backend is connected") {
		t.Errorf("bob has nothing connected: %s", data)
	}
}

// A backend that asks for no token is used as it is.
func TestBackendsWithoutAuthAreUsedWithoutAToken(t *testing.T) {
	open := newMCP(t, &fakeMCP{tools: []string{"Open"}, callReply: map[string]string{"Open": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	w := newWorld(t, map[int]bool{0: true}, open)
	_, data := w.post(aliceToken, listBody, nil)
	if got := strings.Join(toolNames(t, data), ","); got != "Open" {
		t.Fatalf("tools = %s", got)
	}
	w.post(aliceToken, callBody(1, "Open"), nil)
	for _, s := range open.allRequests() {
		if s.header.Get("Authorization") != "" {
			t.Error("a backend configured with auth none was sent a token")
		}
	}
}

// ---- what must never be written down ----------------------------------------------------------------------------

// invariant: no body, no token of any kind is written to the log.
func TestLogsCarryNoBodiesAndNoTokens(t *testing.T) {
	secret := "SELECT salary FROM payroll WHERE ssn = '900101-1234567'"
	reply := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"홍길동 연봉 99,000,000"}]}}`
	plain := newMCP(t, &fakeMCP{tools: []string{"Query"}, callReply: map[string]string{"Query": reply}})
	w := newWorld(t, nil, plain)
	w.as.mu.Lock()
	w.as.expiresIn = 10
	w.as.mu.Unlock()
	w.connect("alice", aliceToken, "b0")

	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "Query", "arguments": map[string]string{"sql": secret}}})
	w.post(aliceToken, string(b), nil)
	w.post("not-a-token", listBody, nil)

	out := waitForLog(t, w.logs, `"tool":"Query"`)
	leaks := []string{"payroll", "900101", "홍길동", "99,000,000", aliceToken, "not-a-token", "s3cret"}
	w.as.mu.Lock()
	for tok := range w.as.tokens {
		leaks = append(leaks, tok)
	}
	for rt := range w.as.refreshTokens {
		leaks = append(leaks, rt)
	}
	w.as.mu.Unlock()
	for _, leak := range leaks {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q", leak)
		}
	}
}

func TestGrantsAreNotStoredInTheClear(t *testing.T) {
	w := newWorld(t, nil, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	w.connect("alice", aliceToken, "b0")
	_, _, tok := w.as.snapshot()
	raw, err := os.ReadFile(w.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) || strings.Contains(string(raw), "alice") || strings.Contains(string(raw), "refresh") {
		t.Error("the state file holds readable content")
	}
	st, err := state.Open(w.statePath, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get(mustPerson(t, w, "alice"), "b0"); !ok {
		t.Error("the grant did not survive a restart")
	}
	if _, err := state.Open(w.statePath, []byte("another secret that is long enough to pass")); err == nil {
		t.Error("the state opened with the wrong secret")
	}
}

// mustPerson computes the key mcp-fuse uses for a person as the authorization server vouches for them.
func mustPerson(t *testing.T, w *world, sub string) string {
	t.Helper()
	p, err := w.f.auth.Verify(context.Background(), map[string]string{"alice": aliceToken, "bob": bobToken}[sub])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The request id is the one header besides the protocol's that a backend is sent. A caller's own id is passed on
// only if it is plainly an id.
func TestRequestIDIsPassedOnOnlyWhenItIsPlainlyAnID(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorld(t, nil, plain)
	w.connect("alice", aliceToken, "b0")

	w.post(aliceToken, listBody, map[string]string{"X-Request-Id": "req-42.a_b"})
	w.post(aliceToken, listBody, map[string]string{"X-Request-Id": "bad id; drop table"})
	w.post(aliceToken, listBody, nil)
	var ids []string
	for _, s := range plain.requests("tools/list") {
		ids = append(ids, s.header.Get("X-Request-Id"))
	}
	if len(ids) != 3 || ids[0] != "req-42.a_b" {
		t.Fatalf("ids = %v", ids)
	}
	for _, id := range ids[1:] {
		if !validRequestID.MatchString(id) || strings.Contains(id, "drop") {
			t.Errorf("a bad or missing caller id must be replaced, got %q", id)
		}
	}
}

// A client pre-registered at the backend's authorization server, with a secret: no self-registration, and the
// secret authenticates the client at the token endpoint for the code and for every refresh.
func TestPreRegisteredConfidentialClient(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}, callReply: map[string]string{"T": `{"jsonrpc":"2.0","id":1,"result":{}}`}})
	w := newWorldWith(t, nil, func(c *config.Config, d *Deps, as *fakeAS) {
		c.Backends[0].ClientID = "pre-registered"
		c.Backends[0].ClientSecretFile = "unused-in-tests"
		d.ClientSecrets["b0"] = "pre-secret"
		as.confidential["pre-registered"] = "pre-secret"
		as.expiresIn = 10 // forces a refresh, which must authenticate too
	}, plain)
	w.connect("alice", aliceToken, "b0")
	if _, regs, _ := w.as.snapshot(); regs != 0 {
		t.Errorf("a pre-registered client must not register itself, registrations = %d", regs)
	}
	if resp, data := w.post(aliceToken, callBody(1, "T"), nil); resp.StatusCode != 200 || !strings.Contains(string(data), `"result"`) {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if calls, _, _ := w.as.snapshot(); calls == 0 {
		t.Error("the refresh did not happen")
	}
}

func TestConfidentialClientWithAWrongSecretCannotConnect(t *testing.T) {
	w := newWorldWith(t, nil, func(c *config.Config, d *Deps, as *fakeAS) {
		c.Backends[0].ClientID = "pre-registered"
		c.Backends[0].ClientSecretFile = "unused-in-tests"
		d.ClientSecrets["b0"] = "the wrong secret"
		as.confidential["pre-registered"] = "pre-secret"
	}, newMCP(t, &fakeMCP{tools: []string{"T"}}))
	_, data := w.post(aliceToken, initBody, nil)
	link := regexp.MustCompile(`http://\S+/connect/b0\?t=\S+`).FindString(instructionsOf(t, data))
	resp, err := newBrowser().Get(link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 || w.f.connected(mustPerson(t, w, "alice"), 0) {
		t.Errorf("connected with the wrong secret: %d", resp.StatusCode)
	}
}

func TestSecretInTheBodyWhenTheServerOnlyTakesThat(t *testing.T) {
	plain := newMCP(t, &fakeMCP{tools: []string{"T"}})
	w := newWorldWith(t, nil, func(c *config.Config, d *Deps, as *fakeAS) {
		c.Backends[0].ClientID = "pre-registered"
		c.Backends[0].ClientSecretFile = "unused-in-tests"
		d.ClientSecrets["b0"] = "pre-secret"
		as.confidential["pre-registered"] = "pre-secret"
		as.authMethods = []string{"client_secret_post"}
		as.secretInBody = true
	}, plain)
	w.connect("alice", aliceToken, "b0")
}
