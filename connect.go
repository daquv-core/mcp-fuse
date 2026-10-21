package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The pages where a person connects a backend. The link handed out in the instructions carries a ticket that
// names a person and a backend - but a ticket only says who the link was made for, and anyone can be handed a
// link. So a connect has two steps, and both are bound to the browser that began it:
//
//  1. identify: the person signs in at the authorization server callers of mcp-fuse log in at, and the person
//     that sign-in proves must be the one the ticket names. The ticket is a claim; this is the proof.
//  2. backend: the person signs in at the backend's own authorization server and the grant is stored.
//
// A cookie set when the link is opened ties both steps to that browser. Without it a link could be started by
// one person and its backend step finished by another, whose token would then be stored as the first one's.

type pendingAuth struct {
	kind     string // kindIdentify or kindBackend
	person   string
	idx      int
	nonce    string // identifies the browser the flow began in
	verifier string
	info     authInfo
	clientID string
	expires  time.Time
}

const (
	pendingTTL   = 10 * time.Minute
	cookieName   = "mcp-fuse-connect"
	kindIdentify = "identify"
	kindBackend  = "backend"
	identifyPath = "/identify"
	callbackPath = "/callback"
)

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
func (f *Fuse) takePending(state, kind string) (pendingAuth, bool) {
	f.pendMu.Lock()
	defer f.pendMu.Unlock()
	p, ok := f.pending[state]
	delete(f.pending, state)
	if !ok || p.kind != kind || time.Now().After(p.expires) {
		return pendingAuth{}, false
	}
	return p, true
}

func (f *Fuse) connectURL(person, backend string) string {
	t := signTicket(f.secret, ticket{Person: person, Backend: backend, Expires: time.Now().Add(ticketTTL).Unix()})
	return f.cfg.origin + f.cfg.connectBase + "/" + url.PathEscape(backend) + "?t=" + url.QueryEscape(t)
}

func (f *Fuse) identifyURI() string { return f.cfg.origin + f.cfg.connectBase + identifyPath }

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
	switch rest := "/" + strings.TrimPrefix(r.URL.Path, f.cfg.connectBase+"/"); rest {
	case identifyPath:
		f.connectIdentify(w, r)
	case callbackPath:
		f.connectCallback(w, r)
	default:
		for i, b := range f.cfg.Backends {
			if "/"+b.Name == rest && b.oauth() {
				f.connectStart(w, r, i)
				return
			}
		}
		http.NotFound(w, r)
	}
}

func (f *Fuse) setFlowCookie(w http.ResponseWriter, nonce string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: nonce, Path: f.cfg.connectBase, MaxAge: maxAge,
		HttpOnly: true, Secure: strings.HasPrefix(f.cfg.origin, "https://"),
		// Lax: the authorization servers send the browser back with a top-level GET, which Lax allows and
		// which a cross-site request in the background is not.
		SameSite: http.SameSiteLaxMode,
	})
}

// sameBrowser says whether the request comes from the browser the flow began in.
func sameBrowser(r *http.Request, nonce string) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && nonce != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(nonce)) == 1
}

func newPKCE() (verifier, challenge string) {
	verifier = randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, b64(sum[:])
}

// authorizeURL builds the authorization request for either step.
func authorizeURL(endpoint string, params map[string]string) (string, error) {
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

// connectStart opens the link: it pins the flow to this browser and sends the person to prove who they are.
func (f *Fuse) connectStart(w http.ResponseWriter, r *http.Request, idx int) {
	bk := f.cfg.Backends[idx]
	t, err := verifyTicket(f.secret, r.URL.Query().Get("t"), time.Now())
	if err != nil || t.Backend != bk.Name {
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	front, err := f.frontAS(r.Context())
	if err != nil {
		f.log.Warn("connect: sign-in server unavailable", "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}

	nonce := randomToken(16)
	state := randomToken(16)
	verifier, challenge := newPKCE()
	f.addPending(state, pendingAuth{kind: kindIdentify, person: t.Person, idx: idx, nonce: nonce, verifier: verifier,
		info: front, expires: time.Now().Add(pendingTTL)})
	target, err := authorizeURL(front.authEndpoint, map[string]string{
		"response_type": "code", "client_id": f.cfg.Auth.ClientID, "redirect_uri": f.identifyURI(),
		"code_challenge": challenge, "code_challenge_method": "S256", "state": state, "resource": f.cfg.Resource,
	})
	if err != nil {
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	f.setFlowCookie(w, nonce, int(pendingTTL.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// connectIdentify is where the sign-in at the callers' authorization server returns. The token it yields is
// used once, to learn who signed in, and dropped; only if that is the person the link names does the backend
// step begin.
func (f *Fuse) connectIdentify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := f.takePending(q.Get("state"), kindIdentify)
	if !ok || !sameBrowser(r, p.nonce) {
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	bk := f.cfg.Backends[p.idx]
	if q.Get("error") != "" || q.Get("code") == "" {
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	if iss := q.Get("iss"); (iss == "" && p.info.issParam) || (iss != "" && iss != p.info.issuer) {
		f.log.Warn("connect: sign-in response from an unexpected issuer")
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	tr, err := f.postToken(r.Context(), p.info.tokenEndpoint, urlValues(
		"grant_type", "authorization_code", "code", q.Get("code"), "redirect_uri", f.identifyURI(),
		"client_id", f.cfg.Auth.ClientID, "code_verifier", p.verifier, "resource", f.cfg.Resource,
	), chooseClientAuth(f.frontSecret, p.info.authMethods))
	if err != nil {
		f.log.Warn("connect: sign-in code exchange failed", "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	who, err := f.auth.verify(r.Context(), tr.AccessToken)
	if err != nil {
		f.log.Warn("connect: could not establish who signed in")
		connectPage(w, http.StatusForbidden, false)
		return
	}
	if subtle.ConstantTimeCompare([]byte(who), []byte(p.person)) != 1 {
		f.log.Warn("connect: the person who signed in is not the one the link was made for", "backend", bk.Name)
		f.setFlowCookie(w, "", -1)
		connectPage(w, http.StatusForbidden, false)
		return
	}

	info, err := f.discover(r.Context(), p.idx)
	if err != nil {
		f.log.Warn("connect: discovery failed", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	clientID, err := f.clientID(r.Context(), p.idx, info)
	if err != nil {
		f.log.Warn("connect: no client identity", "backend", bk.Name, "error", err.Error())
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	state := randomToken(16)
	verifier, challenge := newPKCE()
	f.addPending(state, pendingAuth{kind: kindBackend, person: p.person, idx: p.idx, nonce: p.nonce, verifier: verifier,
		info: info, clientID: clientID, expires: time.Now().Add(pendingTTL)})
	target, err := authorizeURL(info.authEndpoint, map[string]string{
		"response_type": "code", "client_id": clientID, "redirect_uri": f.redirectURI(),
		"code_challenge": challenge, "code_challenge_method": "S256", "state": state,
		"resource": info.resource, "scope": info.scope,
	})
	if err != nil {
		connectPage(w, http.StatusBadGateway, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// connectCallback is where the backend's authorization server sends the person back. The grant is stored for the
// person the identify step proved - and only if this is the browser the flow began in.
func (f *Fuse) connectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := f.takePending(q.Get("state"), kindBackend)
	if !ok || !sameBrowser(r, p.nonce) {
		if ok {
			f.log.Warn("connect: a flow was finished in a different browser than it began in")
		}
		connectPage(w, http.StatusBadRequest, false)
		return
	}
	f.setFlowCookie(w, "", -1)
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

	tr, err := f.postToken(r.Context(), p.info.tokenEndpoint, urlValues(
		"grant_type", "authorization_code", "code", q.Get("code"), "redirect_uri", f.redirectURI(),
		"client_id", p.clientID, "code_verifier", p.verifier, "resource", p.info.resource,
	), f.clientAuthFor(p.idx, p.info))
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

func urlValues(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// connectPage is all the person sees of mcp-fuse. The text is fixed: nothing from the request or from an
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
