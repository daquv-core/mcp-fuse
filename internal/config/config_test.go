package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.MCPPath != "/mcp" || c.PRMPath != "/.well-known/oauth-protected-resource/mcp" ||
		c.PRMURL != "https://mcp.example.com/.well-known/oauth-protected-resource/mcp" ||
		c.ConnectBase != "/mcp/connect" || c.Origin != "https://mcp.example.com" {
		t.Errorf("paths: %q %q %q %q %q", c.MCPPath, c.PRMPath, c.PRMURL, c.ConnectBase, c.Origin)
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
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackendsWithoutOAuthNeedNoPeopleOrState(t *testing.T) {
	c := Config{
		Resource: "https://mcp.example.com/mcp", AuthorizationServers: []string{"https://auth.example.com"},
		Backends: []Backend{{Name: "open", URL: "http://127.0.0.1:9000/mcp", Auth: "none"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFileRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"resource":"https://m.example.com/mcp","authorization_servers":["https://a.example.com"],
		"backends":[{"name":"x","url":"http://127.0.0.1:1/mcp","auth":"none"}],"list_timeout_second":5}`), 0o600)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "list_timeout_second") {
		t.Fatalf("a misspelled key must be an error, got %v", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := Load("../../config.example.json"); err != nil {
		t.Fatal(err)
	}
}
