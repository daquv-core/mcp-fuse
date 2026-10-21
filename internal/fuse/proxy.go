package fuse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/daquv-core/mcp-fuse/internal/jsonrpc"
	"github.com/daquv-core/mcp-fuse/internal/oauthc"
)

// Headers that describe one hop and must not be copied to the next (RFC 9110 §7.6.1).
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// copyHeaders copies a backend's response headers to ours, minus the hop-by-hop ones.
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
	env           jsonrpc.Envelope
	err           error
	notConnected  bool // the person has no (or no longer a working) grant for this backend
	methodMissing bool
}

// rpc sends body to one backend and reads the whole reply. It is for requests whose reply is merged, so the
// reply is parsed; a tool call, whose reply is not read, goes through forward instead.
func (f *Fuse) rpc(ctx context.Context, idx int, c *call, body []byte, wantID json.RawMessage) reply {
	resp, err := f.do(ctx, c.person, idx, body, true)
	if err != nil {
		return reply{err: err, notConnected: errors.Is(err, oauthc.ErrNotConnected)}
	}
	defer resp.Body.Close()

	rep := reply{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode/100 != 2 {
		rep.err = fmt.Errorf("status %d", resp.StatusCode)
		return rep
	}
	if rep.env, rep.err = readFrame(resp, wantID); rep.err != nil {
		return rep
	}
	if len(rep.env.Error) > 0 {
		var e jsonrpc.Error
		_ = json.Unmarshal(rep.env.Error, &e)
		rep.methodMissing = e.Code == jsonrpc.CodeMethodNotFound
		// The message is not logged or returned: a backend's error text can carry the caller's data.
		rep.err = fmt.Errorf("rpc error %d", e.Code)
	}
	return rep
}

// forward relays a request to one backend and streams the answer back untouched.
func (f *Fuse) forward(w http.ResponseWriter, c *call, idx int, label string) {
	bk := f.cfg.Backends[idx]
	resp, err := f.do(c.in.Context(), c.person, idx, c.body, true)
	if err != nil {
		if errors.Is(err, oauthc.ErrNotConnected) {
			jsonrpc.WriteError(w, http.StatusOK, c.env.ID, jsonrpc.CodeNotConnected, "backend "+bk.Name+" is not connected for this user")
			return
		}
		f.log.Warn("backend failed", "backend", bk.Name, "method", c.env.Method, "request_id", c.reqID, "error", err.Error())
		jsonrpc.WriteError(w, http.StatusBadGateway, c.env.ID, jsonrpc.CodeBackend, "backend unavailable")
		return
	}
	defer resp.Body.Close()

	h := w.Header()
	copyHeaders(h, resp.Header)
	h.Del(sessionHeader) // the session is between mcp-fuse and the backend
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

// writeUnauthenticated answers a request without a usable token. The challenge points at this server's own
// metadata document: an agent logs in where this server says, never where a backend does.
func (f *Fuse) writeUnauthenticated(w http.ResponseWriter, id json.RawMessage, hadToken bool) {
	challenge := `Bearer resource_metadata="` + f.cfg.PRMURL + `"`
	if hadToken {
		challenge = `Bearer error="invalid_token", resource_metadata="` + f.cfg.PRMURL + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	jsonrpc.WriteError(w, http.StatusUnauthorized, id, jsonrpc.CodeUnauthorized, "authentication required")
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
