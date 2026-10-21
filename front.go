package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The token a caller presents to mcp-fuse is for mcp-fuse. It is checked here, it says who the caller is, and
// it goes no further: no backend ever sees it.

var (
	errUnauthenticated = errors.New("unauthenticated")
	errAuthUnavailable = errors.New("authentication unavailable")
)

// verifier turns the bearer token of a request into a stable key for the person behind it.
type verifier interface {
	verify(ctx context.Context, token string) (person string, err error)
}

// anonymous is used when no backend needs a person: there is nothing to key and nothing to protect.
type anonymous struct{}

func (anonymous) verify(context.Context, string) (string, error) { return "anonymous", nil }

// introspector asks the authorization server whether a token is live and for whom (RFC 7662). The audience must
// be this server: a token issued for another resource is not accepted here, whoever issued it.
type introspector struct {
	endpoint, clientID, clientSecret, resource string
	hc                                         *http.Client
	now                                        func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]cachedPerson
}

type cachedPerson struct {
	person string
	until  time.Time
}

const (
	introspectionCacheFor = 60 * time.Second
	introspectionCacheMax = 4096
)

func newIntrospector(endpoint, clientID, clientSecret, resource string) *introspector {
	return &introspector{
		endpoint: endpoint, clientID: clientID, clientSecret: clientSecret, resource: resource,
		hc:    &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}},
		now:   time.Now,
		cache: map[[32]byte]cachedPerson{},
	}
}

func (v *introspector) verify(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errUnauthenticated
	}
	key := sha256.Sum256([]byte(token))
	v.mu.Lock()
	if c, ok := v.cache[key]; ok && v.now().Before(c.until) {
		v.mu.Unlock()
		return c.person, nil
	}
	v.mu.Unlock()

	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	if v.clientSecret == "" {
		form.Set("client_id", v.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errAuthUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if v.clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(v.clientID), url.QueryEscape(v.clientSecret))
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		return "", errAuthUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errAuthUnavailable
	}
	var out struct {
		Active bool            `json:"active"`
		Sub    string          `json:"sub"`
		Iss    string          `json:"iss"`
		Aud    json.RawMessage `json:"aud"`
		Exp    int64           `json:"exp"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", errAuthUnavailable
	}
	if !out.Active || out.Sub == "" || !audienceIncludes(out.Aud, v.resource) {
		return "", errUnauthenticated
	}

	sum := sha256.Sum256([]byte(out.Iss + "\n" + out.Sub))
	person := hex.EncodeToString(sum[:])
	until := v.now().Add(introspectionCacheFor)
	if out.Exp > 0 {
		if exp := time.Unix(out.Exp, 0); exp.Before(until) {
			until = exp
		}
	}
	v.mu.Lock()
	if len(v.cache) >= introspectionCacheMax {
		v.cache = map[[32]byte]cachedPerson{}
	}
	v.cache[key] = cachedPerson{person: person, until: until}
	v.mu.Unlock()
	return person, nil
}

// audienceIncludes reads `aud`, which is a string or a list of them.
func audienceIncludes(raw json.RawMessage, resource string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return trimSlash(one) == trimSlash(resource)
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if trimSlash(a) == trimSlash(resource) {
				return true
			}
		}
	}
	return false
}

func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
