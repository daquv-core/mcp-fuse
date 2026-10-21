// Package jsonrpc is the JSON-RPC 2.0 frame MCP rides on, and the few ways this program writes one.
package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Envelope is the JSON-RPC frame. Only the frame is read; params and result stay raw so that nothing inside a
// tool call is re-encoded on its way through.
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// IsRequest says whether the frame is a request, which expects an answer, rather than a notification.
func (e Envelope) IsRequest() bool { return e.Method != "" && len(e.ID) > 0 }

// Error is the error member of a response.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeUnauthorized   = -32001
	CodeBackend        = -32002
	CodeNotConnected   = -32004
)

// MaxFrame is the largest reply read in full.
const MaxFrame = 32 << 20

// WriteResult answers a request with a result.
func WriteResult(w http.ResponseWriter, id json.RawMessage, result any) {
	WriteFrame(w, http.StatusOK, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", idOrNull(id), result})
}

// WriteError answers with an error at the given HTTP status.
func WriteError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	WriteFrame(w, status, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   Error           `json:"error"`
	}{"2.0", idOrNull(id), Error{code, message}})
}

// WriteFrame writes any value as a JSON response.
func WriteFrame(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// ParamString reads one string member of params without touching the rest.
func ParamString(params json.RawMessage, key string) string {
	if len(params) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return ""
	}
	return s
}

// FromSSE returns the response frame carrying wantID from a text/event-stream body. Frames that are
// notifications or requests from the server are skipped.
func FromSSE(r io.Reader, wantID json.RawMessage) (Envelope, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), MaxFrame)
	var data []string
	flush := func() (Envelope, bool) {
		if len(data) == 0 {
			return Envelope{}, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var env Envelope
		if json.Unmarshal([]byte(payload), &env) != nil || env.Method != "" {
			return Envelope{}, false
		}
		if !bytes.Equal(bytes.TrimSpace(env.ID), bytes.TrimSpace(wantID)) {
			return Envelope{}, false
		}
		return env, true
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if env, ok := flush(); ok {
				return env, nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return Envelope{}, err
	}
	if env, ok := flush(); ok {
		return env, nil
	}
	return Envelope{}, errors.New("no response frame in event stream")
}
