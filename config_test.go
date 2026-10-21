package main

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
		Backends: []Backend{
			{Name: "quvi", URL: "http://127.0.0.1:8081/mcp", Stateful: true},
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
		c.prmURL != "https://mcp.example.com/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("paths: %q %q %q", c.mcpPath, c.prmPath, c.prmURL)
	}
	if c.Listen != "127.0.0.1:8080" || c.ListTimeoutSeconds != 15 {
		t.Errorf("defaults: %q %d", c.Listen, c.ListTimeoutSeconds)
	}
}

func TestConfigRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"duplicate name":    func(c *Config) { c.Backends[1].Name = "quvi" },
		"two stateful":      func(c *Config) { c.Backends[1].Stateful = true },
		"no backends":       func(c *Config) { c.Backends = nil },
		"no auth server":    func(c *Config) { c.AuthorizationServers = nil },
		"relative resource": func(c *Config) { c.Resource = "/mcp" },
		"bad backend url":   func(c *Config) { c.Backends[0].URL = "quvi:8081" },
		"empty name":        func(c *Config) { c.Backends[0].Name = "" },
	}
	for name, mutate := range cases {
		c := validConfig()
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestConfigFileRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"resource":"https://m.example.com/mcp","authorization_servers":["https://a.example.com"],
		"backends":[{"name":"x","url":"http://127.0.0.1:1/mcp"}],"list_timeout_second":5}`), 0o600)
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
