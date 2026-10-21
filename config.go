package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Listen               string    `json:"listen"`
	Resource             string    `json:"resource"`
	AuthorizationServers []string  `json:"authorization_servers"`
	Backends             []Backend `json:"backends"`
	ListTimeoutSeconds   int       `json:"list_timeout_seconds"`

	mcpPath string // path the MCP endpoint is served on, taken from Resource
	prmPath string // path of the protected resource metadata document (RFC 9728)
	prmURL  string // absolute URL of that document, advertised in 401 challenges
}

type Backend struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Stateful bool   `json:"stateful"`
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a misspelled key must not silently fall back to a default
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.ListTimeoutSeconds < 0 {
		return fmt.Errorf("list_timeout_seconds must not be negative")
	}
	if c.ListTimeoutSeconds == 0 {
		c.ListTimeoutSeconds = 15
	}

	res, err := httpURL("resource", c.Resource)
	if err != nil {
		return err
	}
	c.mcpPath = strings.TrimRight(res.Path, "/")
	if c.mcpPath == "" {
		c.mcpPath = "/"
	}
	c.prmPath = "/.well-known/oauth-protected-resource"
	if c.mcpPath != "/" {
		c.prmPath += c.mcpPath
	}
	c.prmURL = res.Scheme + "://" + res.Host + c.prmPath

	if len(c.AuthorizationServers) == 0 {
		return fmt.Errorf("authorization_servers must list at least one authorization server")
	}
	for _, as := range c.AuthorizationServers {
		if _, err := httpURL("authorization_servers", as); err != nil {
			return err
		}
	}

	if len(c.Backends) == 0 {
		return fmt.Errorf("backends must list at least one backend")
	}
	names := map[string]bool{}
	stateful := 0
	for _, b := range c.Backends {
		if b.Name == "" {
			return fmt.Errorf("every backend needs a name")
		}
		if names[b.Name] {
			return fmt.Errorf("backend name %q is used twice", b.Name)
		}
		names[b.Name] = true
		if _, err := httpURL("backends["+b.Name+"].url", b.URL); err != nil {
			return err
		}
		if b.Stateful {
			stateful++
		}
	}
	if stateful > 1 {
		return fmt.Errorf("at most one backend may be stateful, found %d", stateful)
	}
	return nil
}

func httpURL(field, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute http(s) URL, got %q", field, raw)
	}
	return u, nil
}
