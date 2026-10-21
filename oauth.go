package main

import (
	"bytes"
	"context"
	"crypto/rand"
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
)

// This file is mcp-fuse as an OAuth client of its backends. To a backend it is an ordinary MCP client: it finds
// the backend's protected resource metadata (RFC 9728), the authorization server named there (RFC 8414),
// registers itself if it has to (RFC 7591), and obtains a token for one person by authorization code with PKCE,
// bound to the backend as the audience (RFC 8707). The token a caller presented to mcp-fuse is never involved.

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b64(b)
}

type authInfo struct {
	resource      string
	issuer        string
	authEndpoint  string
	tokenEndpoint string
	regEndpoint   string
	scope         string
	issParam      bool     // the authorization server says it adds `iss` to authorization responses (RFC 9207)
	authMethods   []string // token_endpoint_auth_methods_supported; empty means the default, client_secret_basic
	fetched       time.Time
}

const discoveryTTL = 10 * time.Minute

var (
	resourceMetadataValue = regexp.MustCompile(`resource_metadata="([^"]*)"`)
	scopeValue            = regexp.MustCompile(`scope="([^"]*)"`)

	errNotConnected = errors.New("not connected")
	errUnauthorized = errors.New("unauthorized")
	errInvalidGrant = errors.New("invalid_grant")
)

func trimSlash(s string) string { return strings.TrimRight(s, "/") }

func (f *Fuse) fetchJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.aux.Do(req)
	if err != nil {
		return errors.New(errKind(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// discover learns where to authorize for one backend. The authorization server is whichever one the backend
// itself names: mcp-fuse does not choose it, and it is not the one agents log in at.
func (f *Fuse) discover(ctx context.Context, idx int) (authInfo, error) {
	f.discMu.Lock()
	defer f.discMu.Unlock()
	if a, ok := f.disc[idx]; ok && time.Since(a.fetched) < discoveryTTL {
		return a, nil
	}
	bk := f.cfg.Backends[idx]

	prmURL, scope := f.probeResourceMetadata(ctx, bk)
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := f.fetchJSON(ctx, prmURL, &prm); err != nil {
		return authInfo{}, fmt.Errorf("protected resource metadata: %w", err)
	}
	if trimSlash(prm.Resource) != trimSlash(bk.URL) {
		return authInfo{}, errors.New("protected resource metadata names a different resource")
	}
	if len(prm.AuthorizationServers) == 0 {
		return authInfo{}, errors.New("protected resource metadata names no authorization server")
	}
	a, err := f.asMetadata(ctx, prm.AuthorizationServers[0])
	if err != nil {
		return authInfo{}, err
	}
	if scope == "" {
		scope = strings.Join(prm.ScopesSupported, " ")
	}
	a.resource, a.scope = prm.Resource, scope
	f.disc[idx] = a
	return a, nil
}

// asMetadata reads an authorization server's metadata (RFC 8414) and refuses one that cannot be used safely.
func (f *Fuse) asMetadata(ctx context.Context, issuer string) (authInfo, error) {
	var meta struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
		IssParam              bool     `json:"authorization_response_iss_parameter_supported"`
		AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := f.fetchAuthorizationServerMetadata(ctx, issuer, &meta); err != nil {
		return authInfo{}, fmt.Errorf("authorization server metadata: %w", err)
	}
	if trimSlash(meta.Issuer) != trimSlash(issuer) {
		return authInfo{}, errors.New("authorization server metadata names a different issuer")
	}
	// Without PKCE support advertised the authorization code would be unprotected; refuse rather than guess.
	pkce := false
	for _, m := range meta.CodeChallengeMethods {
		pkce = pkce || m == "S256"
	}
	if !pkce {
		return authInfo{}, errors.New("authorization server does not advertise PKCE S256")
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return authInfo{}, errors.New("authorization server metadata lacks an endpoint")
	}
	return authInfo{
		issuer: issuer, authEndpoint: meta.AuthorizationEndpoint, tokenEndpoint: meta.TokenEndpoint,
		regEndpoint: meta.RegistrationEndpoint, issParam: meta.IssParam, authMethods: meta.AuthMethods,
		fetched: time.Now(),
	}, nil
}

// frontAS is the authorization server callers of mcp-fuse log in at. A person who opens a connect link signs in
// there first, so that the link is completed by the person it names and by nobody else.
func (f *Fuse) frontAS(ctx context.Context) (authInfo, error) {
	f.discMu.Lock()
	defer f.discMu.Unlock()
	if f.front.issuer != "" && time.Since(f.front.fetched) < discoveryTTL {
		return f.front, nil
	}
	a, err := f.asMetadata(ctx, f.cfg.AuthorizationServers[0])
	if err != nil {
		return authInfo{}, err
	}
	f.front = a
	return a, nil
}

// probeResourceMetadata asks the backend without a token, which is how the MCP authorization specification has a
// client learn the metadata address, and falls back to the well-known location.
func (f *Fuse) probeResourceMetadata(ctx context.Context, bk Backend) (metadataURL, scope string) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"mcp-fuse","version":%q}}}`, latestSupported, f.version)
	if req, err := http.NewRequestWithContext(ctx, http.MethodPost, bk.URL, strings.NewReader(body)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if resp, err := f.aux.Do(req); err == nil {
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

func (f *Fuse) fetchAuthorizationServerMetadata(ctx context.Context, issuer string, out any) error {
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
	for _, c := range candidates {
		if last = f.fetchJSON(ctx, c, out); last == nil {
			return nil
		}
	}
	return last
}

func (f *Fuse) redirectURI() string { return f.cfg.origin + f.cfg.connectBase + "/callback" }

// clientID returns the identity mcp-fuse uses at one backend's authorization server: configured, or registered
// once and remembered.
func (f *Fuse) clientID(ctx context.Context, idx int, a authInfo) (string, error) {
	bk := f.cfg.Backends[idx]
	if bk.ClientID != "" {
		return bk.ClientID, nil
	}
	f.regMu.Lock()
	defer f.regMu.Unlock()
	if id, ok := f.store.client(bk.Name, a.issuer); ok {
		return id, nil
	}
	if a.regEndpoint == "" {
		return "", errors.New("the authorization server offers no client registration; set client_id for this backend")
	}
	reg, _ := json.Marshal(map[string]any{
		"client_name":                "mcp-fuse",
		"redirect_uris":              []string{f.redirectURI()},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.regEndpoint, bytes.NewReader(reg))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.aux.Do(req)
	if err != nil {
		return "", errors.New(errKind(err))
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
	if err := f.store.putClient(bk.Name, a.issuer, out.ClientID); err != nil {
		return "", err
	}
	return out.ClientID, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// clientAuth says how mcp-fuse proves who it is at a backend's token endpoint: a public client sends only its id;
// a confidential one also sends its secret, in the way the server says it takes it (basic by default).
type clientAuth struct {
	secret string
	post   bool // send the secret in the body rather than in a Basic header
}

func (f *Fuse) clientAuthFor(idx int, a authInfo) clientAuth {
	return chooseClientAuth(f.clientSecrets[f.cfg.Backends[idx].Name], a.authMethods)
}

func chooseClientAuth(secret string, methods []string) clientAuth {
	if secret == "" {
		return clientAuth{}
	}
	basic := len(methods) == 0
	post := false
	for _, m := range methods {
		basic = basic || m == "client_secret_basic"
		post = post || m == "client_secret_post"
	}
	return clientAuth{secret: secret, post: !basic && post}
}

// postToken calls a token endpoint. Error text from the server is not carried into ours: only its code.
func (f *Fuse) postToken(ctx context.Context, endpoint string, form url.Values, ca clientAuth) (tokenResponse, error) {
	if ca.secret != "" && ca.post {
		form.Set("client_secret", ca.secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if ca.secret != "" && !ca.post {
		req.SetBasicAuth(url.QueryEscape(form.Get("client_id")), url.QueryEscape(ca.secret))
	}
	resp, err := f.aux.Do(req)
	if err != nil {
		return tokenResponse{}, errors.New(errKind(err))
	}
	defer resp.Body.Close()
	var tr tokenResponse
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr)
	if resp.StatusCode != http.StatusOK {
		if tr.Error == "invalid_grant" {
			return tokenResponse{}, errInvalidGrant
		}
		return tokenResponse{}, fmt.Errorf("token endpoint: status %d %s", resp.StatusCode, tr.Error)
	}
	if !strings.EqualFold(tr.TokenType, "bearer") || tr.AccessToken == "" {
		return tokenResponse{}, errors.New("token endpoint returned no bearer token")
	}
	return tr, nil
}

func grantFrom(tr tokenResponse, prev *grant, clientID, endpoint, resource string, now time.Time) grant {
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 300 // not stated: assume short, so that the next use refreshes rather than trusting a stale token
	}
	g := grant{
		Access: tr.AccessToken, Refresh: tr.RefreshToken, Expiry: now.Add(time.Duration(ttl) * time.Second),
		ClientID: clientID, TokenEndpoint: endpoint, Resource: resource,
	}
	if g.Refresh == "" && prev != nil {
		g.Refresh = prev.Refresh // a server that does not rotate refresh tokens does not repeat them
	}
	return g
}

const refreshMargin = 30 * time.Second

// accessToken returns a token for the person at the backend, refreshing it first if it is about to lapse.
func (f *Fuse) accessToken(ctx context.Context, person string, idx int) (string, error) {
	bk := f.cfg.Backends[idx]
	g, ok := f.store.grant(person, bk.Name)
	if !ok {
		return "", errNotConnected
	}
	if time.Until(g.Expiry) > refreshMargin {
		return g.Access, nil
	}
	return f.refresh(ctx, person, idx, g.Access)
}

// refresh replaces the access token the caller found unusable (stale). If someone else has replaced it in the
// meantime that one is used: with rotating refresh tokens a second refresh would be refused.
func (f *Fuse) refresh(ctx context.Context, person string, idx int, stale string) (string, error) {
	bk := f.cfg.Backends[idx]
	mu := f.keyLock("refresh|" + person + "|" + bk.Name)
	mu.Lock()
	defer mu.Unlock()

	g, ok := f.store.grant(person, bk.Name)
	if !ok {
		return "", errNotConnected
	}
	if g.Access != stale && time.Until(g.Expiry) > refreshMargin {
		return g.Access, nil
	}
	if g.Refresh == "" {
		_ = f.store.deleteGrant(person, bk.Name)
		return "", errNotConnected
	}
	var ca clientAuth
	if a, derr := f.discover(ctx, idx); derr == nil {
		ca = f.clientAuthFor(idx, a)
	} else if f.clientSecrets[bk.Name] != "" {
		return "", derr // a confidential client cannot refresh without knowing how to authenticate
	}
	tr, err := f.postToken(ctx, g.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}, "client_id": {g.ClientID}, "resource": {g.Resource},
	}, ca)
	if errors.Is(err, errInvalidGrant) {
		_ = f.store.deleteGrant(person, bk.Name)
		f.dropSession(sessionKey{person, idx})
		return "", errNotConnected
	}
	if err != nil {
		return "", err
	}
	ng := grantFrom(tr, &g, g.ClientID, g.TokenEndpoint, g.Resource, time.Now())
	if err := f.store.putGrant(person, bk.Name, ng); err != nil {
		return "", err
	}
	return ng.Access, nil
}

// keyLock returns the one mutex for a key, so that work on the same person and backend is serialized.
func (f *Fuse) keyLock(key string) *sync.Mutex {
	mu, _ := f.locks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
