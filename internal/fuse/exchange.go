package fuse

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daquv-core/mcp-fuse/internal/oauthc"
)

// A backend configured "exchange" trusts the authorization server callers log in at. Its token is not given by the
// person: mcp-fuse exchanges the token the caller presented (RFC 8693) at the server that issued it, as the client it
// introspects with, for a token whose audience is the backend. The caller's token goes there and nowhere else - never
// to a backend - and the token the backend receives is issued for that backend alone, so this is not token
// passthrough. Nobody connects such a backend; nothing about it is stored, and a lost token is exchanged again.

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	exchangeMargin     = 30 * time.Second
	maxExchanged       = 10000
)

// callerTokenKey carries the token the caller presented, for the one use made of it besides introspection: the
// subject of a token exchange at the authorization server that issued it.
type callerTokenKey struct{}

// errExchangeRefused: a backend refused a token that was just exchanged for it.
var errExchangeRefused = errors.New("backend refused the exchanged token")

type exchangedToken struct {
	access string
	expiry time.Time
}

type exchangeCache struct {
	mu sync.Mutex
	m  map[sessionKey]exchangedToken
}

func (c *exchangeCache) get(k sessionKey) (exchangedToken, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	return e, ok
}

func (c *exchangeCache) put(k sessionKey, e exchangedToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= maxExchanged {
		c.m = map[sessionKey]exchangedToken{} // an exchange is cheap to repeat; losing them costs one request each
	}
	c.m[k] = e
}

func (c *exchangeCache) drop(k sessionKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}

// exchanged returns the person's token for an "exchange" backend, exchanging the caller's token when there is none
// that will last, or when the one there is the one the backend just refused (stale).
func (f *Fuse) exchanged(ctx context.Context, person string, idx int, stale string) (string, error) {
	key := sessionKey{person, idx}
	mu := f.keyLock("exchange|" + person + "|" + strconv.Itoa(idx))
	mu.Lock()
	defer mu.Unlock()
	if e, ok := f.exchanges.get(key); ok && e.access != stale && time.Until(e.expiry) > exchangeMargin {
		return e.access, nil
	}
	subject, _ := ctx.Value(callerTokenKey{}).(string)
	if subject == "" {
		return "", errors.New("token exchange: no caller token")
	}
	bk, err := f.oc.Discover(ctx, idx)
	if err != nil {
		return "", err
	}
	front, err := f.oc.AS(ctx, f.cfg.AuthorizationServers[0])
	if err != nil {
		return "", err
	}
	// The exchanged token is only good at the backend if the backend trusts the server that issues it.
	if strings.TrimRight(bk.Issuer, "/") != strings.TrimRight(front.Issuer, "/") {
		return "", errors.New("token exchange: the backend names another authorization server than callers log in at")
	}
	scope := f.cfg.Backends[idx].Scope
	if scope == "" {
		scope = bk.Scope
	}
	form := url.Values{
		"grant_type":           {grantTokenExchange},
		"subject_token":        {subject},
		"subject_token_type":   {tokenTypeAccess},
		"requested_token_type": {tokenTypeAccess},
		"client_id":            {f.cfg.Auth.ClientID},
		"resource":             {bk.Resource},
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	tr, err := f.oc.PostToken(ctx, front.TokenEndpoint, form, oauthc.ChooseClientAuth(f.frontSecret, front.AuthMethods))
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 60 // not stated: keep it briefly, the next use exchanges again
	}
	f.exchanges.put(key, exchangedToken{access: tr.AccessToken, expiry: time.Now().Add(time.Duration(ttl) * time.Second)})
	return tr.AccessToken, nil
}
