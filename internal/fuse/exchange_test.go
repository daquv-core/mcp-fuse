package fuse

import (
	"strings"
	"testing"

	"github.com/daquv-core/mcp-fuse/internal/config"
)

// exchangeWorld puts the backends behind mcp-fuse with auth "exchange": they trust the server callers log in at.
func exchangeWorld(t *testing.T, scope string, backends ...*fakeMCP) *world {
	t.Helper()
	return newWorldWith(t, nil, func(c *config.Config, _ *Deps, _ *fakeAS) {
		c.Auth.ClientSecretFile = "read-by-main-not-by-tests"
		for i := range c.Backends {
			c.Backends[i].Auth, c.Backends[i].Scope = "exchange", scope
		}
	}, backends...)
}

func TestExchangeBackendNeedsNoConnect(t *testing.T) {
	b := newMCP(t, &fakeMCP{tools: []string{"write_row"}, sessions: true})
	w := exchangeWorld(t, "registry", b)

	_, data := w.post(aliceToken, initBody, nil)
	if ins := instructionsOf(t, data); strings.Contains(ins, "/connect/") {
		t.Fatalf("an exchange backend was offered a connect link: %s", ins)
	}
	_, data = w.post(aliceToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if got := toolNames(t, data); len(got) != 1 || got[0] != "write_row" {
		t.Fatalf("tools = %v", got)
	}

	ex := w.as.exchangeRequests()
	if len(ex) != 1 {
		t.Fatalf("exchanges = %d, want 1 (the token is kept while it lasts)", len(ex))
	}
	e := ex[0]
	if e.Get("subject_token") != aliceToken || e.Get("resource") != b.url() || e.Get("scope") != "registry" ||
		e.Get("grant_type") != grantTokenExchange || e.Get("requested_token_type") != tokenTypeAccess {
		t.Fatalf("exchange request = %v", e)
	}
	// The caller's token went to the server that issued it and never to the backend.
	for _, s := range b.allRequests() {
		if strings.Contains(s.header.Get("Authorization"), aliceToken) {
			t.Fatalf("the caller's token reached the backend")
		}
	}
}

func TestExchangeIsPerPerson(t *testing.T) {
	b := newMCP(t, &fakeMCP{tools: []string{"t"}})
	w := exchangeWorld(t, "", b)
	w.post(aliceToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	w.post(bobToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	ex := w.as.exchangeRequests()
	if len(ex) != 2 || ex[0].Get("subject_token") != aliceToken || ex[1].Get("subject_token") != bobToken {
		t.Fatalf("exchanges = %v", ex)
	}
	if ex[0].Get("scope") != "" {
		t.Fatalf("no scope configured and none in the metadata, yet scope %q was asked", ex[0].Get("scope"))
	}
}

func TestExchangeRenewsOnceWhenRefused(t *testing.T) {
	b := newMCP(t, &fakeMCP{tools: []string{"t"}})
	w := exchangeWorld(t, "", b)
	w.post(aliceToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)

	b.mu.Lock()
	b.revoke = true
	b.mu.Unlock()
	_, data := w.post(aliceToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if !strings.Contains(string(data), "no backend available") {
		t.Fatalf("a backend that refuses every token answered: %s", data)
	}
	if n := len(w.as.exchangeRequests()); n != 2 {
		t.Fatalf("exchanges = %d, want 2 (one more after the refusal, then give up)", n)
	}
	waitForLog(t, w.logs, errExchangeRefused.Error())

	// A refusal leaves no link to connect: there is nothing a person could do.
	_, data = w.post(aliceToken, initBody, nil)
	if strings.Contains(string(data), "/connect/") {
		t.Fatalf("a refused exchange backend offered a connect link: %s", data)
	}
}

func TestExchangeFailureLeavesOtherBackends(t *testing.T) {
	ok := newMCP(t, &fakeMCP{tools: []string{"open_tool"}})
	ex := newMCP(t, &fakeMCP{tools: []string{"exchanged_tool"}})
	w := newWorldWith(t, map[int]bool{0: true}, func(c *config.Config, _ *Deps, as *fakeAS) {
		c.Auth.ClientSecretFile = "x"
		c.Backends[1].Auth = "exchange"
		as.rejectExchange = true
	}, ok, ex)
	_, data := w.post(aliceToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if got := toolNames(t, data); len(got) != 1 || got[0] != "open_tool" {
		t.Fatalf("tools = %v", got)
	}
	out := waitForLog(t, w.logs, "token exchange")
	if strings.Contains(out, aliceToken) {
		t.Fatalf("a token was logged: %s", out)
	}
}

func TestExchangeOnlyWithTheCallersServer(t *testing.T) {
	other := newAS(t)
	b := newMCP(t, &fakeMCP{tools: []string{"t"}})
	w := exchangeWorld(t, "", b)
	b.as = other // the backend now names, and trusts, a server callers do not log in at
	_, data := w.post(aliceToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if !strings.Contains(string(data), "no backend available") {
		t.Fatalf("answered: %s", data)
	}
	if n := len(w.as.exchangeRequests()); n != 0 {
		t.Fatalf("the caller's token was exchanged for a backend that does not trust its issuer (%d)", n)
	}
	waitForLog(t, w.logs, "another authorization server")
}
