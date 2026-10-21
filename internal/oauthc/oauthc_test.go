package oauthc

import (
	"crypto/sha256"
	"net/url"
	"testing"
	"time"

	"github.com/daquv-core/mcp-fuse/internal/state"
)

func TestPKCEChallengeIsTheS256OfTheVerifier(t *testing.T) {
	v, c := NewPKCE()
	sum := sha256.Sum256([]byte(v))
	if c != b64(sum[:]) || len(v) < 43 {
		t.Errorf("verifier %q challenge %q", v, c)
	}
	if v2, _ := NewPKCE(); v2 == v {
		t.Error("verifiers repeat")
	}
}

func TestChooseClientAuth(t *testing.T) {
	cases := []struct {
		secret  string
		methods []string
		want    ClientAuth
	}{
		{"", nil, ClientAuth{}},
		{"s", nil, ClientAuth{secret: "s"}},
		{"s", []string{"none", "client_secret_basic"}, ClientAuth{secret: "s"}},
		{"s", []string{"client_secret_post"}, ClientAuth{secret: "s", post: true}},
		{"s", []string{"client_secret_post", "client_secret_basic"}, ClientAuth{secret: "s"}},
	}
	for _, c := range cases {
		if got := ChooseClientAuth(c.secret, c.methods); got != c.want {
			t.Errorf("ChooseClientAuth(%q, %v) = %+v, want %+v", c.secret, c.methods, got, c.want)
		}
	}
}

func TestGrantFromKeepsTheOldRefreshTokenWhenNoneIsRepeated(t *testing.T) {
	now := time.Unix(1000, 0)
	prev := state.Grant{Refresh: "old"}
	g := GrantFrom(TokenResponse{AccessToken: "a", ExpiresIn: 60}, &prev, "c", "https://t", "https://r", now)
	if g.Refresh != "old" || g.Access != "a" || !g.Expiry.Equal(now.Add(time.Minute)) {
		t.Errorf("%+v", g)
	}
	g = GrantFrom(TokenResponse{AccessToken: "a", RefreshToken: "new"}, &prev, "c", "https://t", "https://r", now)
	if g.Refresh != "new" || !g.Expiry.Equal(now.Add(5*time.Minute)) {
		t.Errorf("an unstated lifetime must be short, and a repeated refresh token replaces the old: %+v", g)
	}
}

func TestAuthorizeURLLeavesOutEmptyParametersAndKeepsTheQuery(t *testing.T) {
	got, err := AuthorizeURL("https://as.example/authorize?tenant=x", map[string]string{"state": "s", "scope": "", "resource": "https://r"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if q.Get("tenant") != "x" || q.Get("state") != "s" || q.Get("resource") != "https://r" || q.Has("scope") {
		t.Errorf("%s", got)
	}
}
