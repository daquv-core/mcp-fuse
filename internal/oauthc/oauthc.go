// Package oauthc is mcp-fuse as an OAuth client of its backends. To a backend it is an ordinary MCP client: it
// finds the backend's protected resource metadata (RFC 9728), the authorization server named there (RFC 8414),
// registers itself if it has to (RFC 7591), and obtains a token for one person by authorization code with PKCE,
// bound to the backend as the audience (RFC 8707). The token a caller presented to mcp-fuse is never involved.
package oauthc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daquv-core/mcp-fuse/internal/config"
	"github.com/daquv-core/mcp-fuse/internal/neterr"
	"github.com/daquv-core/mcp-fuse/internal/state"
)

var (
	// ErrNotConnected: the person has no grant for the backend, or the one they had no longer works.
	ErrNotConnected = errors.New("not connected")
	// ErrInvalidGrant: a token endpoint refused a code or a refresh token.
	ErrInvalidGrant = errors.New("invalid_grant")
)

// Info is what is known about where to authorize for one backend, or for the callers' authorization server.
type Info struct {
	Resource      string // the audience to ask tokens for (backends only)
	Issuer        string
	AuthEndpoint  string
	TokenEndpoint string
	RegEndpoint   string
	Scope         string
	IssParam      bool     // the server says it adds `iss` to authorization responses (RFC 9207)
	AuthMethods   []string // token_endpoint_auth_methods_supported; empty means the default, client_secret_basic
	fetched       time.Time
}

const (
	discoveryTTL  = 10 * time.Minute
	refreshMargin = 30 * time.Second
)

// Client holds what mcp-fuse needs to act as an OAuth client of its backends.
type Client struct {
	HTTP        *http.Client // discovery, registration and token endpoints
	Store       *state.Store
	Backends    []config.Backend
	Secrets     map[string]string // backend name -> secret of its pre-registered confidential client
	Version     string
	Probe       string // protocol revision to name when probing a backend for its metadata address
	RedirectURI string // where the backends' authorization servers send a person back
	// OnRevoked is called when a grant is dropped because its authorization server refused the refresh token, so
	// that whatever was built on that grant (a session) can be dropped too.
	OnRevoked func(person string, idx int)

	discMu sync.Mutex
	disc   map[int]Info
	as     map[string]Info
	regMu  sync.Mutex
	locks  sync.Map // key -> *sync.Mutex
}

var (
	resourceMetadataValue = regexp.MustCompile(`resource_metadata="([^"]*)"`)
	scopeValue            = regexp.MustCompile(`scope="([^"]*)"`)
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// RandomToken returns n random bytes, URL-safe encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b64(b)
}

// NewPKCE returns a code verifier and its S256 challenge (RFC 7636).
func NewPKCE() (verifier, challenge string) {
	verifier = RandomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, b64(sum[:])
}

// AuthorizeURL builds an authorization request. Empty parameters are left out.
func AuthorizeURL(endpoint string, params map[string]string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }

func (c *Client) fetchJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New(neterr.Kind(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Discover learns where to authorize for one backend. The authorization server is whichever one the backend
// itself names: mcp-fuse does not choose it, and it is not the one agents log in at.
func (c *Client) Discover(ctx context.Context, idx int) (Info, error) {
	c.discMu.Lock()
	defer c.discMu.Unlock()
	if c.disc == nil {
		c.disc = map[int]Info{}
	}
	if a, ok := c.disc[idx]; ok && time.Since(a.fetched) < discoveryTTL {
		return a, nil
	}
	bk := c.Backends[idx]

	prmURL, scope := c.probeResourceMetadata(ctx, bk)
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := c.fetchJSON(ctx, prmURL, &prm); err != nil {
		return Info{}, fmt.Errorf("protected resource metadata: %w", err)
	}
	if trimSlash(prm.Resource) != trimSlash(bk.URL) {
		return Info{}, errors.New("protected resource metadata names a different resource")
	}
	if len(prm.AuthorizationServers) == 0 {
		return Info{}, errors.New("protected resource metadata names no authorization server")
	}
	a, err := c.metadata(ctx, prm.AuthorizationServers[0])
	if err != nil {
		return Info{}, err
	}
	if scope == "" {
		scope = strings.Join(prm.ScopesSupported, " ")
	}
	a.Resource, a.Scope = prm.Resource, scope
	c.disc[idx] = a
	return a, nil
}

// AS reads the metadata of an authorization server that is not tied to a backend: the one callers of mcp-fuse
// log in at. The result is kept for a while.
func (c *Client) AS(ctx context.Context, issuer string) (Info, error) {
	c.discMu.Lock()
	defer c.discMu.Unlock()
	if c.as == nil {
		c.as = map[string]Info{}
	}
	if a, ok := c.as[issuer]; ok && time.Since(a.fetched) < discoveryTTL {
		return a, nil
	}
	a, err := c.metadata(ctx, issuer)
	if err != nil {
		return Info{}, err
	}
	c.as[issuer] = a
	return a, nil
}

// metadata reads an authorization server's metadata (RFC 8414) and refuses one that cannot be used safely.
func (c *Client) metadata(ctx context.Context, issuer string) (Info, error) {
	var meta struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
		IssParam              bool     `json:"authorization_response_iss_parameter_supported"`
		AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := c.fetchAuthorizationServerMetadata(ctx, issuer, &meta); err != nil {
		return Info{}, fmt.Errorf("authorization server metadata: %w", err)
	}
	if trimSlash(meta.Issuer) != trimSlash(issuer) {
		return Info{}, errors.New("authorization server metadata names a different issuer")
	}
	// Without PKCE support advertised the authorization code would be unprotected; refuse rather than guess.
	pkce := false
	for _, m := range meta.CodeChallengeMethods {
		pkce = pkce || m == "S256"
	}
	if !pkce {
		return Info{}, errors.New("authorization server does not advertise PKCE S256")
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return Info{}, errors.New("authorization server metadata lacks an endpoint")
	}
	return Info{
		Issuer: issuer, AuthEndpoint: meta.AuthorizationEndpoint, TokenEndpoint: meta.TokenEndpoint,
		RegEndpoint: meta.RegistrationEndpoint, IssParam: meta.IssParam, AuthMethods: meta.AuthMethods,
		fetched: time.Now(),
	}, nil
}

// probeResourceMetadata asks the backend without a token, which is how the MCP authorization specification has a
// client learn the metadata address, and falls back to the well-known location.
func (c *Client) probeResourceMetadata(ctx context.Context, bk config.Backend) (metadataURL, scope string) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"mcp-fuse","version":%q}}}`, c.Probe, c.Version)
	if req, err := http.NewRequestWithContext(ctx, http.MethodPost, bk.URL, strings.NewReader(body)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if resp, err := c.HTTP.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				challenge := resp.Header.Get("WWW-Authenticate")
				if m := scopeValue.FindStringSubmatch(challenge); m != nil {
					scope = m[1]
				}
				if m := resourceMetadataValue.FindStringSubmatch(challenge); m != nil {
					return m[1], scope
				}
			}
		}
	}
	u, _ := url.Parse(bk.URL)
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + strings.TrimRight(u.Path, "/"), scope
}

func (c *Client) fetchAuthorizationServerMetadata(ctx context.Context, issuer string, out any) error {
	u, err := url.Parse(issuer)
	if err != nil {
		return err
	}
	base := u.Scheme + "://" + u.Host
	path := strings.TrimRight(u.Path, "/")
	candidates := []string{
		base + "/.well-known/oauth-authorization-server" + path,
		base + "/.well-known/openid-configuration" + path,
		base + path + "/.well-known/openid-configuration",
	}
	var last error
	for _, cand := range candidates {
		if last = c.fetchJSON(ctx, cand, out); last == nil {
			return nil
		}
	}
	return last
}

// ClientID returns the identity mcp-fuse uses at one backend's authorization server: configured, or registered
// once and remembered.
func (c *Client) ClientID(ctx context.Context, idx int, a Info) (string, error) {
	bk := c.Backends[idx]
	if bk.ClientID != "" {
		return bk.ClientID, nil
	}
	c.regMu.Lock()
	defer c.regMu.Unlock()
	if id, ok := c.Store.Client(bk.Name, a.Issuer); ok {
		return id, nil
	}
	if a.RegEndpoint == "" {
		return "", errors.New("the authorization server offers no client registration; set client_id for this backend")
	}
	reg, _ := json.Marshal(map[string]any{
		"client_name":                "mcp-fuse",
		"redirect_uris":              []string{c.RedirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.RegEndpoint, bytes.NewReader(reg))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", errors.New(neterr.Kind(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("client registration refused: status %d", resp.StatusCode)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.ClientID == "" {
		return "", errors.New("client registration returned no client_id")
	}
	if err := c.Store.PutClient(bk.Name, a.Issuer, out.ClientID); err != nil {
		return "", err
	}
	return out.ClientID, nil
}

// TokenResponse is what a token endpoint returns.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// ClientAuth says how mcp-fuse proves who it is at a token endpoint: a public client sends only its id; a
// confidential one also sends its secret, in the way the server says it takes it (basic by default).
type ClientAuth struct {
	secret string
	post   bool // send the secret in the body rather than in a Basic header
}

// ClientAuthFor is the way to authenticate at one backend's token endpoint.
func (c *Client) ClientAuthFor(idx int, a Info) ClientAuth {
	return ChooseClientAuth(c.Secrets[c.Backends[idx].Name], a.AuthMethods)
}

// ChooseClientAuth picks the way to send a secret that a server's advertised methods allow.
func ChooseClientAuth(secret string, methods []string) ClientAuth {
	if secret == "" {
		return ClientAuth{}
	}
	basic := len(methods) == 0
	post := false
	for _, m := range methods {
		basic = basic || m == "client_secret_basic"
		post = post || m == "client_secret_post"
	}
	return ClientAuth{secret: secret, post: !basic && post}
}

// PostToken calls a token endpoint. Error text from the server is not carried into ours: only its code.
func (c *Client) PostToken(ctx context.Context, endpoint string, form url.Values, ca ClientAuth) (TokenResponse, error) {
	if ca.secret != "" && ca.post {
		form.Set("client_secret", ca.secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if ca.secret != "" && !ca.post {
		req.SetBasicAuth(url.QueryEscape(form.Get("client_id")), url.QueryEscape(ca.secret))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return TokenResponse{}, errors.New(neterr.Kind(err))
	}
	defer resp.Body.Close()
	var tr TokenResponse
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr)
	if resp.StatusCode != http.StatusOK {
		if tr.Error == "invalid_grant" {
			return TokenResponse{}, ErrInvalidGrant
		}
		return TokenResponse{}, fmt.Errorf("token endpoint: status %d %s", resp.StatusCode, tr.Error)
	}
	if !strings.EqualFold(tr.TokenType, "bearer") || tr.AccessToken == "" {
		return TokenResponse{}, errors.New("token endpoint returned no bearer token")
	}
	return tr, nil
}

// GrantFrom turns a token response into a grant. A server that does not rotate refresh tokens does not repeat
// them, so the previous one is kept.
func GrantFrom(tr TokenResponse, prev *state.Grant, clientID, endpoint, resource string, now time.Time) state.Grant {
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 300 // not stated: assume short, so that the next use refreshes rather than trusting a stale token
	}
	g := state.Grant{
		Access: tr.AccessToken, Refresh: tr.RefreshToken, Expiry: now.Add(time.Duration(ttl) * time.Second),
		ClientID: clientID, TokenEndpoint: endpoint, Resource: resource,
	}
	if g.Refresh == "" && prev != nil {
		g.Refresh = prev.Refresh
	}
	return g
}

// AccessToken returns a token for the person at the backend, refreshing it first if it is about to lapse.
func (c *Client) AccessToken(ctx context.Context, person string, idx int) (string, error) {
	g, ok := c.Store.Get(person, c.Backends[idx].Name)
	if !ok {
		return "", ErrNotConnected
	}
	if time.Until(g.Expiry) > refreshMargin {
		return g.Access, nil
	}
	return c.Refresh(ctx, person, idx, g.Access)
}

// Refresh replaces the access token the caller found unusable (stale). If someone else has replaced it in the
// meantime that one is used: with rotating refresh tokens a second refresh would be refused.
func (c *Client) Refresh(ctx context.Context, person string, idx int, stale string) (string, error) {
	bk := c.Backends[idx]
	mu := c.keyLock(person + "|" + bk.Name)
	mu.Lock()
	defer mu.Unlock()

	g, ok := c.Store.Get(person, bk.Name)
	if !ok {
		return "", ErrNotConnected
	}
	if g.Access != stale && time.Until(g.Expiry) > refreshMargin {
		return g.Access, nil
	}
	if g.Refresh == "" {
		_ = c.Store.Delete(person, bk.Name)
		return "", ErrNotConnected
	}
	var ca ClientAuth
	if a, derr := c.Discover(ctx, idx); derr == nil {
		ca = c.ClientAuthFor(idx, a)
	} else if c.Secrets[bk.Name] != "" {
		return "", derr // a confidential client cannot refresh without knowing how to authenticate
	}
	tr, err := c.PostToken(ctx, g.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}, "client_id": {g.ClientID}, "resource": {g.Resource},
	}, ca)
	if errors.Is(err, ErrInvalidGrant) {
		_ = c.Store.Delete(person, bk.Name)
		if c.OnRevoked != nil {
			c.OnRevoked(person, idx)
		}
		return "", ErrNotConnected
	}
	if err != nil {
		return "", err
	}
	ng := GrantFrom(tr, &g, g.ClientID, g.TokenEndpoint, g.Resource, time.Now())
	if err := c.Store.Put(person, bk.Name, ng); err != nil {
		return "", err
	}
	return ng.Access, nil
}

// keyLock returns the one mutex for a key, so that work on the same person and backend is serialized.
func (c *Client) keyLock(key string) *sync.Mutex {
	mu, _ := c.locks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
