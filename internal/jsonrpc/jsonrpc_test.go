package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParamStringReadsOneMemberAndNothingElse(t *testing.T) {
	params := json.RawMessage(`{"name":"T","arguments":{"name":"nested"},"n":1}`)
	if got := ParamString(params, "name"); got != "T" {
		t.Errorf("name = %q", got)
	}
	for _, p := range []json.RawMessage{nil, json.RawMessage(`[]`), json.RawMessage(`{"name":3}`)} {
		if got := ParamString(p, "name"); got != "" {
			t.Errorf("ParamString(%s) = %q, want empty", p, got)
		}
	}
}

func TestFromSSEFindsTheResponseCarryingTheID(t *testing.T) {
	stream := "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\r\n\r\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"other\":true}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\n" +
		"data: \"result\":{\"ok\":true}}\n\n"
	env, err := FromSSE(strings.NewReader(stream), json.RawMessage("7"))
	if err != nil || string(env.Result) != `{"ok":true}` {
		t.Fatalf("%v %s", err, env.Result)
	}
	if _, err := FromSSE(strings.NewReader(stream), json.RawMessage("9")); err == nil {
		t.Error("an id that is not in the stream was found")
	}
}

func TestIsRequestSeparatesRequestsFromNotifications(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`:             true,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`: false,
		`{"jsonrpc":"2.0","id":1,"result":{}}`:                 false,
	} {
		var e Envelope
		_ = json.Unmarshal([]byte(raw), &e)
		if e.IsRequest() != want {
			t.Errorf("%s -> %v", raw, e.IsRequest())
		}
	}
}
