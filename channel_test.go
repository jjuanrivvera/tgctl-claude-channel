package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- test doubles ---------------------------------------------------------------------

// fakeTransport records every tgctl invocation so tool dispatch and inbound handling
// are testable without spawning tgctl.
type fakeTransport struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *fakeTransport) record(args ...string) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
}
func (f *fakeTransport) send(chatID, text string) (string, error) {
	f.record("send", chatID, text)
	return "ok", nil
}
func (f *fakeTransport) react(c, m, e string) (string, error) {
	f.record("react", c, m, e)
	return "ok", nil
}
func (f *fakeTransport) edit(c, m, t string) (string, error) {
	f.record("edit", c, m, t)
	return "ok", nil
}
func (f *fakeTransport) action(c, a string) (string, error) {
	f.record("action", c, a)
	return "ok", nil
}
func (f *fakeTransport) cmd(args ...string) (string, error) {
	f.record(args...)
	return "ok message_id=1", nil
}
func (f *fakeTransport) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *fakeTransport) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, c := range f.calls {
		b.WriteString(strings.Join(c, " ") + "\n")
	}
	return b.String()
}

func newTestServer(t *testing.T, allow ...string) (*server, *fakeTransport, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	ft := &fakeTransport{}
	buf := &bytes.Buffer{}
	s := &server{
		out:    newOut(buf),
		tg:     ft,
		typing: newTypingManager(ft),
		store:  newAccessStore(dir, allow, "", false),
		perms:  newPermissionManager(),
		cfg:    Config{TgctlBin: "tgctl", StateDir: dir},
	}
	return s, ft, buf
}

func call(t *testing.T, s *server, name string, args map[string]any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return s.callTool(name, b)
}

// waitForCalls polls until the fake transport has seen at least n invocations, for
// tests that fan out through goroutines.
func waitForCalls(t *testing.T, ft *fakeTransport, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ft.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected >=%d tgctl calls, got %d", n, ft.count())
}

// --- initialize + tools ---------------------------------------------------------------

func TestInitialize_AdvertisesBothCapabilities(t *testing.T) {
	res := initializeResult(json.RawMessage(`{"protocolVersion":"2025-03-26"}`))
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("should echo client protocolVersion, got %v", res["protocolVersion"])
	}
	exp := res["capabilities"].(map[string]any)["experimental"].(map[string]any)
	if _, ok := exp[channelCapability]; !ok {
		t.Errorf("must advertise %q", channelCapability)
	}
	if _, ok := exp[permissionCapability]; !ok {
		t.Errorf("must advertise %q (drops --dangerously-skip-permissions)", permissionCapability)
	}
}

func TestToolsList_HasFullToolbox(t *testing.T) {
	got := map[string]bool{}
	for _, td := range toolDefs() {
		got[td["name"].(string)] = true
	}
	for _, want := range []string{"reply", "react", "edit", "poll", "photo", "document", "dice", "pin", "unpin", "answer_callback", "download_attachment"} {
		if !got[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

// --- callTool + outbound gate ---------------------------------------------------------

func TestReply_AllowedChat_CallsSendMessage(t *testing.T) {
	s, ft, _ := newTestServer(t, "123")
	if _, err := call(t, s, "reply", map[string]any{"chat_id": "123", "text": "hello"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	got := ft.all()
	if !strings.Contains(got, "sendMessage") || !strings.Contains(got, "hello") || !strings.Contains(got, "123") {
		t.Fatalf("reply should call api sendMessage with chat+text; got %q", got)
	}
}

func TestReply_DeniedChat_IsError(t *testing.T) {
	s, ft, _ := newTestServer(t, "123")
	_, err := call(t, s, "reply", map[string]any{"chat_id": "999", "text": "leak"})
	if err == nil {
		t.Fatal("reply to a non-allowlisted chat must be rejected")
	}
	if ft.count() != 0 {
		t.Fatalf("nothing should be sent to a denied chat; got %q", ft.all())
	}
}

func TestReply_LongText_Chunks(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	long := strings.Repeat("x", 5000) // > 4096
	if _, err := call(t, s, "reply", map[string]any{"chat_id": "1", "text": long}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	sends := strings.Count(ft.all(), "sendMessage")
	if sends < 2 {
		t.Fatalf("5000-char reply should split into >=2 messages, got %d", sends)
	}
}

func TestReply_WithButtons_EmitsInlineKeyboard(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	_, err := call(t, s, "reply", map[string]any{
		"chat_id": "1", "text": "pick",
		"buttons": []any{[]any{map[string]any{"text": "A", "callback_data": "a"}}},
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if !strings.Contains(ft.all(), "inline_keyboard") {
		t.Fatalf("buttons should produce a reply_markup; got %q", ft.all())
	}
}

func TestPoll_Dice_AnswerCallback_Download(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	mustCall(t, s, "poll", map[string]any{"chat_id": "1", "question": "q", "options": []string{"a", "b"}})
	mustCall(t, s, "dice", map[string]any{"chat_id": "1"})
	mustCall(t, s, "answer_callback", map[string]any{"callback_query_id": "cb1", "text": "ok"})
	mustCall(t, s, "download_attachment", map[string]any{"file_id": "F1"})
	got := ft.all()
	for _, want := range []string{"sendPoll", "sendDice", "answerCallbackQuery", "file download"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in calls; got %q", want, got)
		}
	}
}

func TestUnknownTool_Errors(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	if _, err := call(t, s, "nope", nil); err == nil {
		t.Fatal("unknown tool must error")
	}
}

func mustCall(t *testing.T, s *server, name string, args map[string]any) {
	t.Helper()
	if _, err := call(t, s, name, args); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// --- pure helpers ---------------------------------------------------------------------

func TestInlineKeyboard(t *testing.T) {
	if inlineKeyboard(nil) != nil || inlineKeyboard(json.RawMessage(`null`)) != nil || inlineKeyboard(json.RawMessage(`[]`)) != nil {
		t.Error("empty/null/[] buttons should yield nil")
	}
	kb := inlineKeyboard(json.RawMessage(`[[{"text":"A","callback_data":"a"}]]`))
	if kb == nil || kb["inline_keyboard"] == nil {
		t.Error("valid buttons should produce inline_keyboard")
	}
}

func TestAsInt(t *testing.T) {
	if n, ok := asInt(" 42 "); !ok || n != 42 {
		t.Errorf("asInt(42)=%d,%v", n, ok)
	}
	if _, ok := asInt("x"); ok {
		t.Error("asInt(x) should fail")
	}
}

func TestIsImageFile(t *testing.T) {
	if !isImageFile("/a/b.PNG") || !isImageFile("x.jpeg") {
		t.Error("image exts should match (case-insensitive)")
	}
	if isImageFile("/a/b.pdf") {
		t.Error("non-image should not match")
	}
}

func TestBufferReceivesJSON(t *testing.T) {
	// sanity: out.send writes newline-delimited JSON
	buf := &bytes.Buffer{}
	o := newOut(buf)
	o.send(map[string]any{"jsonrpc": "2.0"})
	var v map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &v); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	_ = io.Discard
}
