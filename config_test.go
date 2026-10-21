package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Resource:             "https://mcp.example.com/mcp/",
		AuthorizationServers: []string{"https://auth.example.com"},
		Auth:                 Auth{IntrospectionURL: "https://auth.example.com/introspect", ClientID: "fuse"},
		StateFile:            "/var/lib/mcp-fuse/state.bin",
		SecretFile:           "/etc/mcp-fuse/secret",
		Backends: []Backend{
			{Name: "quvi", URL: "http://127.0.0.1:8081/mcp"},
			{Name: "exec", URL: "http://127.0.0.1:9000/mcp"},
		},
	}
}

func TestConfigDerivesPathsAndDefaults(t *testing.T) {
	c := validConfig()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if c.mcpPath != "/mcp" || c.prmPath != "/.well-known/oauth-protected-resource/mcp" ||
		c.prmURL != "https://mcp.example.com/.well-known/oauth-protected-resource/mcp" ||
		c.connectBase != "/mcp/connect" || c.origin != "https://mcp.example.com" {
		t.Errorf("paths: %q %q %q %q %q", c.mcpPath, c.prmPath, c.prmURL, c.connectBase, c.origin)
	}
	if c.Listen != "127.0.0.1:8080" || c.ListTimeoutSeconds != 15 || c.Backends[0].Auth != "oauth" {
		t.Errorf("defaults: %q %d %q", c.Listen, c.ListTimeoutSeconds, c.Backends[0].Auth)
	}
}

func TestConfigRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"duplicate name":            func(c *Config) { c.Backends[1].Name = "quvi" },
		"no backends":               func(c *Config) { c.Backends = nil },
		"no auth server":            func(c *Config) { c.AuthorizationServers = nil },
		"relative resource":         func(c *Config) { c.Resource = "/mcp" },
		"bad backend url":           func(c *Config) { c.Backends[0].URL = "quvi:8081" },
		"empty name":                func(c *Config) { c.Backends[0].Name = "" },
		"name with a slash":         func(c *Config) { c.Backends[0].Name = "a/b" },
		"unknown backend auth":      func(c *Config) { c.Backends[0].Auth = "basic" },
		"oauth without callers":     func(c *Config) { c.Auth = Auth{} },
		"introspection no client":   func(c *Config) { c.Auth.ClientID = "" },
		"oauth without state file":  func(c *Config) { c.StateFile = "" },
		"oauth without secret file": func(c *Config) { c.SecretFile = "" },
		"client secret, no id":      func(c *Config) { c.Backends[0].ClientSecretFile = "/etc/mcp-fuse/quvi-secret" },
	}
	for name, mutate := range cases {
		c := validConfig()
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackendsWithoutOAuthNeedNoPeopleOrState(t *testing.T) {
	c := Config{
		Resource: "https://mcp.example.com/mcp", AuthorizationServers: []string{"https://auth.example.com"},
		Backends: []Backend{{Name: "open", URL: "http://127.0.0.1:9000/mcp", Auth: "none"}},
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFileRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"resource":"https://m.example.com/mcp","authorization_servers":["https://a.example.com"],
		"backends":[{"name":"x","url":"http://127.0.0.1:1/mcp","auth":"none"}],"list_timeout_second":5}`), 0o600)
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "list_timeout_second") {
		t.Fatalf("a misspelled key must be an error, got %v", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := loadConfig("config.example.json"); err != nil {
		t.Fatal(err)
	}
}

func TestTicketsExpireAndCannotBeForged(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	tok := signTicket(secret, ticket{Person: "p", Backend: "b", Expires: now.Add(time.Minute).Unix()})
	if got, err := verifyTicket(secret, tok, now); err != nil || got.Person != "p" || got.Backend != "b" {
		t.Fatalf("valid ticket: %v %v", got, err)
	}
	if _, err := verifyTicket(secret, tok, now.Add(2*time.Minute)); err == nil {
		t.Error("an expired ticket was accepted")
	}
	if _, err := verifyTicket([]byte("another secret, also long enough for this"), tok, now); err == nil {
		t.Error("a ticket signed with another secret was accepted")
	}
	body, sig, _ := strings.Cut(tok, ".")
	if _, err := verifyTicket(secret, body+"x."+sig, now); err == nil {
		t.Error("a ticket with a changed body was accepted")
	}
}

func TestSecretMustBeLongEnough(t *testing.T) {
	if _, err := readSecret(tmpFile(t, "short")); err == nil {
		t.Error("a short secret was accepted")
	}
	if s, err := readSecret(tmpFile(t, "0123456789abcdef0123456789abcdef\n")); err != nil || len(s) != 32 {
		t.Errorf("a long enough secret: %v %d", err, len(s))
	}
}

func TestStateFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.bin")
	st, err := openStore(path, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.putGrant("p", "b", grant{Access: "a"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, %v", info.Mode().Perm(), err)
	}
}
