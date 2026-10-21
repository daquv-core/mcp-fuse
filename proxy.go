package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	maxBody       = 16 << 20 // largest request body read
	maxReply      = 32 << 20 // largest backend reply read in full (lists, initialize)
	sessionHeader = "Mcp-Session-Id"
)

// Headers that describe one hop and must not be copied to the next (RFC 9110 §7.6.1).
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func copyHeaders(dst, src http.Header) {
	listed := map[string]bool{}
	for _, v := range src.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			listed[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for k, vs := range src {
		if hopByHop[k] || listed[k] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// reply is what one backend answered to one request, reduced to what the caller needs.
type reply struct {
	status        int
	header        http.Header
	env           envelope
	err           error
	unauthorized  bool
	methodMissing bool
	sessionGone   bool // the stateful backend no longer knows the session the caller presented
}

// outbound builds the request for one backend. Every header the caller sent is passed on - a header that is
// dropped here is a header the backend never sees - except the ones that belong to this hop. The session
// header goes to the stateful backend only: the id it carries is that backend's and means nothing elsewhere.
func (f *Fuse) outbound(ctx context.Context, idx int, c *call, body []byte) (*http.Request, error) {
	bk := f.cfg.Backends[idx]
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bk.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, c.in.Header)
	for _, h := range []string{"Host", "Content-Length", "Accept-Encoding", sessionHeader, "Content-Type"} {
		req.Header.Del(h)
	}
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if bk.Stateful {
		if sid := c.in.Header.Get(sessionHeader); sid != "" {
			req.Header.Set(sessionHeader, sid)
		}
	}

	peer, _, err := net.SplitHostPort(c.in.RemoteAddr)
	if err != nil {
		peer = c.in.RemoteAddr
	}
	if prior := c.in.Header.Get("X-Forwarded-For"); prior != "" {
		peer = prior + ", " + peer
	}
	req.Header.Set("X-Forwarded-For", peer)
	if c.in.Header.Get("X-Forwarded-Host") == "" {
		req.Header.Set("X-Forwarded-Host", c.in.Host)
	}
	if c.in.Header.Get("X-Forwarded-Proto") == "" {
		proto := "http"
		if c.in.TLS != nil {
			proto = "https"
		}
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	req.Header.Set("X-Request-Id", c.reqID)
	return req, nil
}

// rpc sends body to one backend and reads the whole reply. It is for requests whose reply is merged, so the
// reply is parsed; a tool call, whose reply is not read, goes through forward instead.
func (f *Fuse) rpc(ctx context.Context, idx int, c *call, body []byte, wantID json.RawMessage) reply {
	req, err := f.outbound(ctx, idx, c, body)
	if err != nil {
		return reply{err: err}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return reply{err: errors.New(errKind(err))}
	}
	defer resp.Body.Close()

	rep := reply{status: resp.StatusCode, header: resp.Header}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		rep.unauthorized = true
		rep.err = errors.New("unauthorized")
		return rep
	case resp.StatusCode == http.StatusNotFound && f.cfg.Backends[idx].Stateful && c.in.Header.Get(sessionHeader) != "":
		rep.sessionGone = true
		rep.err = errors.New("session not found")
		return rep
	case resp.StatusCode/100 != 2:
		rep.err = fmt.Errorf("status %d", resp.StatusCode)
		return rep
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		rep.err = err
		return rep
	}
	if len(data) > maxReply {
		rep.err = errors.New("reply too large")
		return rep
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
		rep.env, rep.err = rpcFromSSE(bytes.NewReader(data), wantID)
	} else {
		rep.err = json.Unmarshal(data, &rep.env)
	}
	if rep.err != nil {
		return rep
	}
	if len(rep.env.Error) > 0 {
		var e rpcError
		_ = json.Unmarshal(rep.env.Error, &e)
		rep.methodMissing = e.Code == codeMethodNotFound
		// The message is not logged or returned: a backend's error text can carry the caller's data.
		rep.err = fmt.Errorf("rpc error %d", e.Code)
	}
	return rep
}

// forward relays a request to one backend and streams the answer back untouched.
func (f *Fuse) forward(w http.ResponseWriter, c *call, idx int, label string) {
	bk := f.cfg.Backends[idx]
	req, err := f.outbound(c.in.Context(), idx, c, c.body)
	if err != nil {
		writeRPCError(w, http.StatusBadGateway, c.env.ID, codeBackend, "backend unavailable")
		return
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.log.Warn("backend unreachable", "backend", bk.Name, "method", c.env.Method, "request_id", c.reqID, "error", errKind(err))
		writeRPCError(w, http.StatusBadGateway, c.env.ID, codeBackend, "backend unavailable")
		return
	}
	defer resp.Body.Close()

	h := w.Header()
	copyHeaders(h, resp.Header)
	if !bk.Stateful {
		h.Del(sessionHeader)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		h.Set("WWW-Authenticate", f.challenge(resp.Header.Get("WWW-Authenticate")))
	}
	w.WriteHeader(resp.StatusCode)

	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			break
		}
	}
	f.log.Info("call", "method", c.env.Method, "tool", label, "backend", bk.Name, "status", resp.StatusCode,
		"ms", time.Since(c.start).Milliseconds(), "request_id", c.reqID)
}

var resourceMetadataParam = regexp.MustCompile(`resource_metadata="[^"]*"`)

// challenge rewrites a backend's 401 challenge so that it points at this server's metadata document: a client
// that followed the backend's own address would authorize against a resource it never connected to.
func (f *Fuse) challenge(upstream string) string {
	param := `resource_metadata="` + f.cfg.prmURL + `"`
	switch {
	case upstream == "" || !strings.Contains(upstream, " "):
		return "Bearer " + param
	case resourceMetadataParam.MatchString(upstream):
		return resourceMetadataParam.ReplaceAllLiteralString(upstream, param)
	default:
		return upstream + ", " + param
	}
}

func (f *Fuse) writeUnauthorized(w http.ResponseWriter, id json.RawMessage, upstream http.Header) {
	var challenge string
	if upstream != nil {
		challenge = upstream.Get("WWW-Authenticate")
	}
	w.Header().Set("WWW-Authenticate", f.challenge(challenge))
	writeRPCError(w, http.StatusUnauthorized, id, codeUnauthorized, "authentication required")
}

// errKind keeps the class of a transport error out of the logs without its text, which can carry addresses
// from the request.
func errKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	default:
		return "unreachable"
	}
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
