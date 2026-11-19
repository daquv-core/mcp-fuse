// Package config is the JSON file mcp-fuse is started with, and the checks that make a bad one fail at startup.
package config

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
	Auth                 Auth      `json:"auth"`
	StateFile            string    `json:"state_file"`
	SecretFile           string    `json:"secret_file"`
	Backends             []Backend `json:"backends"`
	ListTimeoutSeconds   int       `json:"list_timeout_seconds"`

	// Derived from Resource by Validate.
	Origin      string `json:"-"` // scheme://host
	MCPPath     string `json:"-"` // path the MCP endpoint is served on
	PRMPath     string `json:"-"` // path of the protected resource metadata document (RFC 9728)
	PRMURL      string `json:"-"` // absolute URL of that document, advertised in 401 challenges
	ConnectBase string `json:"-"` // path prefix of the pages where a person connects a backend
}

// Auth says how the token a caller presents to mcp-fuse is checked. That token is for mcp-fuse alone and is
// never sent on.
type Auth struct {
	IntrospectionURL string `json:"introspection_url"` // RFC 7662; empty turns caller authentication off
	ClientID         string `json:"client_id"`
	ClientSecretFile string `json:"client_secret_file"`
}

type Backend struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Auth is "oauth" (default): mcp-fuse is an OAuth client of this backend's authorization server and holds a
	// token per person, which the person gives once by connecting the backend. "exchange": the backend trusts the
	// authorization server callers log in at, and mcp-fuse exchanges the caller's token there (RFC 8693) for one
	// whose audience is the backend - nobody connects anything. "none": the backend asks for no token.
	Auth string `json:"auth"`
	// ClientID is the identity pre-registered for mcp-fuse at this backend's authorization server. Without it the
	// client registers itself (RFC 7591). ClientSecretFile holds the secret of a confidential pre-registered client.
	// Both are for "oauth" only: an exchange is made as the client of `auth`.
	ClientID         string `json:"client_id"`
	ClientSecretFile string `json:"client_secret_file"`
	// Scope is what an "exchange" backend's token is asked for with. Empty: the scopes the backend's metadata lists.
	Scope string `json:"scope"`
}

// OAuth says whether each person connects this backend.
func (b Backend) OAuth() bool { return b.Auth == "oauth" }

// Exchange says whether this backend's token is obtained by exchanging the caller's.
func (b Backend) Exchange() bool { return b.Auth == "exchange" }

// Token says whether this backend is sent a token at all. A backend configured "none" asks for none.
func (b Backend) Token() bool { return b.Auth != "none" }

// Load reads and validates the configuration file. An unknown key is an error.
func Load(path string) (Config, error) {
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
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate fills in the defaults and the derived fields, and rejects a configuration that cannot work.
func (c *Config) Validate() error {
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
	c.Origin = res.Scheme + "://" + res.Host
	c.MCPPath = strings.TrimRight(res.Path, "/")
	if c.MCPPath == "" {
		c.MCPPath = "/"
	}
	c.PRMPath = "/.well-known/oauth-protected-resource"
	if c.MCPPath != "/" {
		c.PRMPath += c.MCPPath
	}
	c.PRMURL = c.Origin + c.PRMPath
	c.ConnectBase = strings.TrimRight(c.MCPPath, "/") + "/connect"

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
	needsGrants, exchanges := false, false
	for i := range c.Backends {
		b := &c.Backends[i]
		if b.Name == "" || strings.ContainsAny(b.Name, "/?# ") {
			return fmt.Errorf("backend name %q must be non-empty and free of '/', '?', '#' and spaces", b.Name)
		}
		if names[b.Name] {
			return fmt.Errorf("backend name %q is used twice", b.Name)
		}
		names[b.Name] = true
		if _, err := httpURL("backends["+b.Name+"].url", b.URL); err != nil {
			return err
		}
		switch b.Auth {
		case "":
			b.Auth = "oauth"
		case "oauth", "exchange", "none":
		default:
			return fmt.Errorf("backends[%s].auth must be \"oauth\", \"exchange\" or \"none\", got %q", b.Name, b.Auth)
		}
		if b.ClientSecretFile != "" && b.ClientID == "" {
			return fmt.Errorf("backends[%s].client_secret_file needs client_id", b.Name)
		}
		if !b.OAuth() && (b.ClientID != "" || b.ClientSecretFile != "") {
			return fmt.Errorf("backends[%s]: client_id and client_secret_file are for auth \"oauth\" only", b.Name)
		}
		if !b.Exchange() && b.Scope != "" {
			return fmt.Errorf("backends[%s].scope is for auth \"exchange\" only", b.Name)
		}
		needsGrants = needsGrants || b.OAuth()
		exchanges = exchanges || b.Exchange()
	}

	if c.Auth.IntrospectionURL == "" {
		if needsGrants {
			return fmt.Errorf("auth.introspection_url is required: grants are kept per person, so callers must be identified")
		}
		if exchanges {
			return fmt.Errorf("auth.introspection_url is required: a backend with auth \"exchange\" is given the caller's identity")
		}
	} else {
		if _, err := httpURL("auth.introspection_url", c.Auth.IntrospectionURL); err != nil {
			return err
		}
		if c.Auth.ClientID == "" {
			return fmt.Errorf("auth.client_id is required with auth.introspection_url")
		}
	}
	if exchanges && c.Auth.ClientSecretFile == "" {
		return fmt.Errorf("auth.client_secret_file is required: a token exchange is made by a confidential client")
	}
	if needsGrants && (c.StateFile == "" || c.SecretFile == "") {
		return fmt.Errorf("state_file and secret_file are required when a backend uses oauth")
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
