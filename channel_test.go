package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// fakeTransport records outbound calls so tool dispatch is testable without tgctl.
type fakeTransport struct {
	calls    int
	lastArgs []string
}

func (f *fakeTransport) send(_, _ string) (string, error)     { return "ok", nil }
func (f *fakeTransport) react(_, _, _ string) (string, error) { return "ok", nil }
func (f *fakeTransport) edit(_, _, _ string) (string, error)  { return "ok", nil }
func (f *fakeTransport) action(_, _ string) (string, error)   { return "ok", nil }
func (f *fakeTransport) cmd(args ...string) (string, error) {
	f.calls++
	f.lastArgs = args
	return "ok message_id=42", nil
}

func TestBuildNotification_GatesOnSender(t *testing.T) {
	allow := map[int64]bool{456: true}

	got := buildNotification(update{Message: &tgMessage{
		MessageID: 7, Text: "hola",
		Chat: tgChat{ID: 123}, From: tgUser{ID: 456, Username: "juan"},
	}}, allow)
	if got == nil {
		t.Fatal("allowlisted sender should produce a notification")
	}
	m := got.(map[string]any)
	if m["method"] != "notifications/claude/channel" {
		t.Errorf("method = %v, want notifications/claude/channel", m["method"])
	}
	params := m["params"].(map[string]any)
	if params["content"] != "hola" {
		t.Errorf("content = %v, want hola", params["content"])
	}
	meta := params["meta"].(map[string]string)
	if meta["chat_id"] != "123" || meta["user_id"] != "456" || meta["message_id"] != "7" {
		t.Errorf("meta = %v", meta)
	}

	// Gate + dropping rules.
	if buildNotification(update{Message: &tgMessage{Text: "hi", From: tgUser{ID: 999}}}, allow) != nil {
		t.Error("non-allowlisted sender must be dropped")
	}
	if buildNotification(update{Message: &tgMessage{From: tgUser{ID: 456}}}, allow) != nil {
		t.Error("empty text must be dropped")
	}
	if buildNotification(update{}, allow) != nil {
		t.Error("update without message must be dropped")
	}
}

func TestPump_StreamGatesAndEmits(t *testing.T) {
	// Two updates back-to-back, as tgctl emits them on its JSON stream.
	stream := `
{"update_id":1,"message":{"message_id":1,"text":"yes","chat":{"id":10},"from":{"id":456,"username":"juan"}}}
{"update_id":2,"message":{"message_id":2,"text":"no","chat":{"id":10},"from":{"id":999}}}
`
	var emitted []any
	pump(strings.NewReader(stream), map[int64]bool{456: true}, func(v any) { emitted = append(emitted, v) })
	if len(emitted) != 1 {
		t.Fatalf("expected 1 emission (only allowlisted sender), got %d", len(emitted))
	}
}

func TestInitialize_AdvertisesChannelCapability(t *testing.T) {
	res := initializeResult(json.RawMessage(`{"protocolVersion":"2025-03-26"}`))
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("should echo client protocolVersion, got %v", res["protocolVersion"])
	}
	caps := res["capabilities"].(map[string]any)
	exp := caps["experimental"].(map[string]any)
	if _, ok := exp[channelCapability]; !ok {
		t.Errorf("must advertise experimental[%q], got %v", channelCapability, exp)
	}
}

func TestToolsList_HasReplyReactEdit(t *testing.T) {
	names := map[string]bool{}
	for _, td := range toolDefs() {
		names[td["name"].(string)] = true
	}
	for _, want := range []string{"reply", "react", "edit"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestDispatch_ToolCallReply_InvokesTransport(t *testing.T) {
	ft := &fakeTransport{}
	var buf bytes.Buffer
	srv := &server{out: newOut(&buf), tg: ft}

	srv.dispatch(inMsg{
		Method: "tools/call",
		ID:     json.RawMessage(`1`),
		Params: json.RawMessage(`{"name":"reply","arguments":{"chat_id":"123","text":"hello"}}`),
	})

	// reply → api sendMessage with the chat_id + text carried in the JSON body.
	if ft.calls != 1 {
		t.Fatalf("transport.cmd not invoked once: calls=%d", ft.calls)
	}
	joined := strings.Join(ft.lastArgs, " ")
	if !strings.Contains(joined, "sendMessage") || !strings.Contains(joined, "123") || !strings.Contains(joined, "hello") {
		t.Fatalf("reply should call `api sendMessage` with chat+text; got %v", ft.lastArgs)
	}
	var resp map[string]any
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	res := resp["result"].(map[string]any)
	if _, isErr := res["isError"]; isErr {
		t.Errorf("reply should not be an error: %v", res)
	}
}

func TestParseAllow(t *testing.T) {
	got := parseAllow(" 111, 222 ,, 333 ")
	if len(got) != 3 || !got[111] || !got[222] || !got[333] {
		t.Errorf("parseAllow = %v", got)
	}
	if len(parseAllow("")) != 0 {
		t.Error("empty allowlist should parse to empty map")
	}
}
