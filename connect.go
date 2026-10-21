package main

import (
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The pages where a person connects a backend. Nothing here needs a login to mcp-fuse: the link the assistant
// hands over carries a ticket (grants.go) that names the person and the backend, and the backend's own
// authorization server is where the person actually signs in.

type pendingAuth struct {
	person   string
	idx      int
	verifier string
	info     authInfo
	clientID string
	expires  time.Time
}

const pendingTTL = 10 * time.Minute

func (f *Fuse) addPending(state string, p pendingAuth) {
	f.pendMu.Lock()
	defer f.pendMu.Unlock()
	now := time.Now()
	for k, v := range f.pending {
		if now.After(v.expires) {
			delete(f.pending, k)
		}
	}
	f.pending[state] = p
}

// takePending hands out a pending authorization once: a state value cannot be replayed.
func (f *Fuse) takePending(state string) (pendingAuth, bool) {
	f.pendMu.Lock()
	defer f.pendMu.Unlock()
	p, ok := f.pending[state]
	delete(f.pending, state)
	if !ok || time.Now().After(p.expires) {
		return pendingAuth{}, false
	}
	return p, true
}

func (f *Fuse) connectURL(person, backend string) string {
	t := signTicket(f.secret, ticket{Person: person, Backend: backend, Expires: time.Now().Add(ticketTTL).Unix()})
	return f.cfg.origin + f.cfg.connectBase + "/" + url.PathEscape(backend) + "?t=" + url.QueryEscape(t)
}

func (f *Fuse) serveConnect(w http.ResponseWriter, r *http.Request) {
	if f.store == nil {
		http.NotFound(w, r) // no backend uses a token, so there is nothing to connect
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, f.cfg.connectBase+"/")
	if rest == "callback" {
		f.connectCallback(w, r)
		return
	}
	for i, b := range f.cfg.Backends {
		if b.Name == rest && b.oauth() {
			f.connectStart(w, r, i)
			return
		}
	}
	http.NotFound(w, r)
}

func (f *Fuse) connectStart(w http.ResponseWriter, r *http.Request, idx int) {
	bk := f.cfg.Backends[idx]
	t, err := verifyTicket(f.secret, r.URL.Query().Get("t"), time.Now())
	if err != nil || t.Backend != bk.Name {
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	info, err := f.discover(r.Context(), idx)
	if err != nil {
		f.log.Warn("connect: discovery failed", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	clientID, err := f.clientID(r.Context(), idx, info)
	if err != nil {
		f.log.Warn("connect: no client identity", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}

	state := randomToken(16)
	verifier := randomToken(32)
	challenge := b64(sha256Sum(verifier))
	f.addPending(state, pendingAuth{person: t.Person, idx: idx, verifier: verifier, info: info, clientID: clientID, expires: time.Now().Add(pendingTTL)})

	u, err := url.Parse(info.authEndpoint)
	if err != nil {
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", f.redirectURI())
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("resource", info.resource)
	if info.scope != "" {
		q.Set("scope", info.scope)
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *Fuse) connectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := f.takePending(q.Get("state"))
	if !ok {
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	bk := f.cfg.Backends[p.idx]
	if q.Get("error") != "" || q.Get("code") == "" {
		f.log.Info("connect: not granted", "backend", bk.Name, "error", q.Get("error"))
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	// RFC 9207: the response must come from the authorization server the flow began at. A server that says it adds
	// `iss` must have added it; one that adds it must have added the right one.
	if iss := q.Get("iss"); (iss == "" && p.info.issParam) || (iss != "" && iss != p.info.issuer) {
		f.log.Warn("connect: authorization response from an unexpected issuer", "backend", bk.Name)
		connectPage(w, http.StatusBadRequest, false)
		return
	}

	tr, err := f.postToken(r.Context(), p.info.tokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {f.redirectURI()},
		"client_id": {p.clientID}, "code_verifier": {p.verifier}, "resource": {p.info.resource},
	}, f.clientAuthFor(p.idx, p.info))
	if err != nil {
		f.log.Warn("connect: code exchange failed", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	g := grantFrom(tr, nil, p.clientID, p.info.tokenEndpoint, p.info.resource, time.Now())
	if err := f.store.putGrant(p.person, bk.Name, g); err != nil {
		f.log.Error("connect: grant not stored", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusInternalServerError, false)
		return
	}
	f.dropSession(sessionKey{p.person, p.idx}) // the next use opens a session with the new token
	f.log.Info("connect: granted", "backend", bk.Name)
	connectPage(w, http.StatusOK, true)
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// connectPage is all the person sees of mcp-fuse. The text is fixed: nothing from the request or from the
// authorization server is echoed into it.
func connectPage(w http.ResponseWriter, status int, ok bool) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	msg := `<h1>Connected</h1><p>You can close this window and go back to the chat.</p>
<p>연결되었습니다. 이 창을 닫고 대화로 돌아가세요.</p>`
	if !ok {
		msg = `<h1>Could not connect</h1><p>Ask the assistant for a new link and try again.</p>
<p>연결하지 못했습니다. 어시스턴트에게 새 링크를 요청해 다시 시도하세요.</p>`
	}
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>mcp-fuse</title>
<body style="font-family:sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">` + msg))
}
