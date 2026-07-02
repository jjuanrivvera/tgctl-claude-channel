package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDispatch_Methods(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	s.dispatch(inMsg{Method: "initialize", ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)})
	s.dispatch(inMsg{Method: "tools/list", ID: json.RawMessage(`2`)})
	s.dispatch(inMsg{Method: "ping", ID: json.RawMessage(`3`)})
	s.dispatch(inMsg{Method: "notifications/initialized"})
	s.dispatch(inMsg{Method: "bogus", ID: json.RawMessage(`4`)}) // request → method-not-found
	s.dispatch(inMsg{Method: "bogus-notification"})              // notification → silently ignored
	out := buf.String()
	if !strings.Contains(out, "protocolVersion") {
		t.Error("initialize should respond")
	}
	if !strings.Contains(out, "\"tools\"") {
		t.Error("tools/list should respond")
	}
	if !strings.Contains(out, "method not found") {
		t.Error("unknown request should error")
	}
}

func TestDispatch_PermissionRequest(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	s.dispatch(inMsg{Method: "notifications/claude/channel/permission_request",
		Params: json.RawMessage(`{"request_id":"abcde","tool_name":"Bash","description":"d","input_preview":"{}"}`)})
	waitForCalls(t, ft, 1)
	if !strings.Contains(ft.all(), "perm:allow:abcde") {
		t.Error("permission_request should relay Allow/Deny buttons")
	}
}

func TestDispatch_ToolCall(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	s.dispatch(inMsg{Method: "tools/call", ID: json.RawMessage(`5`),
		Params: json.RawMessage(`{"name":"dice","arguments":{"chat_id":"1"}}`)})
	if !strings.Contains(ft.all(), "sendDice") || !strings.Contains(buf.String(), "content") {
		t.Error("a tool call should invoke tgctl and return content")
	}
	// malformed params
	s.dispatch(inMsg{Method: "tools/call", ID: json.RawMessage(`6`), Params: json.RawMessage(`{bad`)})
	// denied tool → an isError result (not a transport error)
	s.dispatch(inMsg{Method: "tools/call", ID: json.RawMessage(`7`),
		Params: json.RawMessage(`{"name":"reply","arguments":{"chat_id":"999","text":"x"}}`)})
	if !strings.Contains(buf.String(), "isError") {
		t.Error("a denied tool should return an isError result")
	}
}

func TestReply_WithFiles(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	mustCall(t, s, "reply", map[string]any{"chat_id": "1", "text": "here", "files": []string{"/a/b.jpg", "/a/c.pdf"}})
	got := ft.all()
	if !strings.Contains(got, "media photo") || !strings.Contains(got, "media document") {
		t.Errorf("image files send as photos, others as documents; got %q", got)
	}
}

func TestHandleMessage_PhotoDelivered(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	s.handleMessage(&message{MessageID: 1, Chat: tgChat{ID: 1, Type: "private"}, From: tgUser{ID: 1}, Photo: []fileRef{{FileID: "p", FileUniqueID: "u"}}})
	if !strings.Contains(ft.all(), "file download") {
		t.Error("an inbound photo should be downloaded")
	}
	if !strings.Contains(buf.String(), "image_path") || !strings.Contains(buf.String(), "(photo)") {
		t.Errorf("photo notification should carry image_path and a (photo) content; got %q", buf.String())
	}
}

func TestWritePID(t *testing.T) {
	dir := t.TempDir()
	writePID(dir)
	b, err := os.ReadFile(filepath.Join(dir, "bot.pid"))
	if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("writePID should record our pid; got %q err=%v", b, err)
	}
}

func TestDefaultStateDir(t *testing.T) {
	if defaultStateDir() == "" {
		t.Error("state dir should never be empty")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("TGCTL_TOKEN", "tok")
	t.Setenv("TGCTL_CHANNEL_ALLOW", "1,2")
	t.Setenv("TGCTL_CHANNEL_STATE_DIR", t.TempDir())
	cfg := loadConfig()
	if cfg.BotToken != "tok" || len(cfg.AllowSeed) != 2 || cfg.OffsetFile == "" {
		t.Fatalf("loadConfig = %+v", cfg)
	}
}
