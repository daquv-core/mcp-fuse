package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// envelope is the JSON-RPC frame. Only the frame is read; params and result stay raw so that nothing inside a
// tool call is re-encoded on its way through.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (e envelope) isRequest() bool { return e.Method != "" && len(e.ID) > 0 }

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeUnauthorized   = -32001
	codeBackend        = -32002
)

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeFrame(w, http.StatusOK, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", idOrNull(id), result})
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeFrame(w, status, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", idOrNull(id), rpcError{code, message}})
}

func writeFrame(w http.ResponseWriter, status int, v any) {
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

// paramString reads one string member of params without touching the rest.
func paramString(params json.RawMessage, key string) string {
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

// rpcFromSSE returns the response frame carrying wantID from a text/event-stream body. Frames that are
// notifications or requests from the server are skipped.
func rpcFromSSE(r io.Reader, wantID json.RawMessage) (envelope, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxReply)
	var data []string
	flush := func() (envelope, bool) {
		if len(data) == 0 {
			return envelope{}, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var env envelope
		if json.Unmarshal([]byte(payload), &env) != nil || env.Method != "" {
			return envelope{}, false
		}
		if !bytes.Equal(bytes.TrimSpace(env.ID), bytes.TrimSpace(wantID)) {
			return envelope{}, false
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
		return envelope{}, err
	}
	if env, ok := flush(); ok {
		return env, nil
	}
	return envelope{}, errors.New("no response frame in event stream")
}
