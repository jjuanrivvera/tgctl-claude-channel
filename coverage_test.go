package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- callTool: remaining tools + gate-error branches ----------------------------------

func TestCallTool_AllTools_HitAPI(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	mustCall(t, s, "edit", map[string]any{"chat_id": "1", "message_id": "5", "text": "new", "parse_mode": "HTML"})
	mustCall(t, s, "react", map[string]any{"chat_id": "1", "message_id": "5", "emoji": "👍"})
	mustCall(t, s, "pin", map[string]any{"chat_id": "1", "message_id": "5", "silent": true})
	mustCall(t, s, "unpin", map[string]any{"chat_id": "1"})
	mustCall(t, s, "photo", map[string]any{"chat_id": "1", "photo": "http://x/y.jpg", "caption": "c"})
	mustCall(t, s, "document", map[string]any{"chat_id": "1", "document": "http://x/y.pdf"})
	got := ft.all()
	for _, want := range []string{"editMessageText", "setMessageReaction", "pinChatMessage", "unpinChatMessage", "media photo", "media document"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in calls", want)
		}
	}
}

func TestCallTool_GateDeniesEveryChatTool(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	for _, name := range []string{"reply", "react", "edit", "poll", "photo", "document", "dice", "pin", "unpin"} {
		args := map[string]any{"chat_id": "999", "text": "x", "message_id": "1", "emoji": "👍",
			"question": "q", "options": []string{"a"}, "photo": "u", "document": "u"}
		if _, err := call(t, s, name, args); err == nil {
			t.Errorf("%s to a denied chat should error", name)
		}
	}
	if ft.count() != 0 {
		t.Fatalf("nothing should reach tgctl for denied chats; got %q", ft.all())
	}
}

func TestCallTool_BadJSON(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	if _, err := s.callTool("reply", json.RawMessage(`{bad`)); err == nil {
		t.Fatal("malformed args should error")
	}
}

// --- commands -------------------------------------------------------------------------

func TestCommands_HelpAndStatus(t *testing.T) {
	s, ft, _ := newTestServer(t, "1") // allowlist seeded with "1"
	s.handleCommand(&message{Text: "/help", From: tgUser{ID: 1}}, "private", "1", "1")
	if !strings.Contains(ft.all(), "/status") {
		t.Error("/help should list commands")
	}
	s.handleCommand(&message{Text: "/status", From: tgUser{ID: 1}}, "private", "1", "1")
	if !strings.Contains(ft.all(), "Paired as 1") {
		t.Error("/status should report paired")
	}
}

func TestCommands_StatusNotPaired(t *testing.T) {
	s, ft, _ := newTestServer(t) // pairing mode, empty allowlist
	s.handleCommand(&message{Text: "/status", From: tgUser{ID: 9}}, "private", "9", "9")
	if !strings.Contains(ft.all(), "Not paired") {
		t.Errorf("unknown user /status → not paired; got %q", ft.all())
	}
}

func TestCommands_StatusPending(t *testing.T) {
	s, ft, _ := newTestServer(t)
	s.store.update(func(a *access) bool {
		a.Pending["abcdef"] = pendingEntry{SenderID: "9"}
		return true
	})
	s.handleCommand(&message{Text: "/status", From: tgUser{ID: 9}}, "private", "9", "9")
	if !strings.Contains(ft.all(), "abcdef") {
		t.Errorf("pending user /status → the code; got %q", ft.all())
	}
}

func TestCommands_GroupIgnored(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	s.handleCommand(&message{Text: "/start", From: tgUser{ID: 1}}, "group", "1", "-100")
	if ft.count() != 0 {
		t.Fatal("commands must be ignored in groups")
	}
}

func TestCommands_DisabledIgnored(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	s.store.update(func(a *access) bool { a.DMPolicy = policyDisabled; return true })
	s.handleCommand(&message{Text: "/start", From: tgUser{ID: 1}}, "private", "1", "1")
	if ft.count() != 0 {
		t.Fatal("disabled policy → no command replies")
	}
}

// --- pairing reply path in handleMessage ----------------------------------------------

func TestHandleMessage_PairingReplies(t *testing.T) {
	s, ft, buf := newTestServer(t) // pairing mode
	s.handleMessage(&message{MessageID: 1, Text: "hi there", Chat: tgChat{ID: 9, Type: "private"}, From: tgUser{ID: 9}})
	if buf.Len() != 0 {
		t.Fatal("an unpaired DM must not reach the session")
	}
	if !strings.Contains(ft.all(), "pair") {
		t.Errorf("unpaired sender should get a pairing prompt; got %q", ft.all())
	}
}

// --- inbound callback: permission consumed, unauthorized ------------------------------

func TestHandleCallback_PermissionConsumed(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	s.handleCallback(&callbackQuery{ID: "c", Data: "perm:allow:abcde", From: tgUser{ID: 1}, Message: &message{Chat: tgChat{ID: 1}}})
	// a permission button resolves internally and emits a permission decision, not a turn
	if !strings.Contains(buf.String(), "permission") {
		t.Errorf("permission tap should emit a decision; got %q", buf.String())
	}
	if strings.Contains(buf.String(), "\"content\":\"[button tap]") {
		t.Error("a permission tap must not also surface as a channel turn")
	}
}

func TestHandleCallback_Unauthorized(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	s.handleCallback(&callbackQuery{ID: "c", Data: "pick_a", From: tgUser{ID: 999}, Message: &message{Chat: tgChat{ID: 999}}})
	if buf.Len() != 0 {
		t.Fatal("a tap from a non-allowlisted user must not surface")
	}
	if !strings.Contains(ft.all(), "Not authorized") {
		t.Error("unauthorized tap should be answered")
	}
}

// --- permission "more" expansion ------------------------------------------------------

func TestPermission_More(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	s.perms.pending["abcde"] = permDetail{toolName: "Bash", description: "run", inputPreview: `{"cmd":"ls"}`}
	cq := &callbackQuery{ID: "c", Data: "perm:more:abcde", From: tgUser{ID: 1}, Message: &message{MessageID: 2, Chat: tgChat{ID: 1}}}
	if !s.perms.handleCallback(s, cq) {
		t.Fatal("more should be consumed")
	}
	if !strings.Contains(ft.all(), "editMessageText") || !strings.Contains(ft.all(), "input_preview") {
		t.Errorf("more should expand the request inline; got %q", ft.all())
	}
}

// --- chunk boundary variants ----------------------------------------------------------

func TestChunk_BoundaryLineAndSpace(t *testing.T) {
	// a single newline (not paragraph) past halfway is a valid cut point
	text := strings.Repeat("a", 70) + "\n" + strings.Repeat("b", 70)
	got := chunk(text, 100, "newline")
	if got[0] != strings.Repeat("a", 70) {
		t.Errorf("should cut at the newline, got %q", got[0])
	}
	// no break at all → hard cut at limit
	hard := chunk(strings.Repeat("a", 150), 100, "newline")
	if len([]rune(hard[0])) != 100 {
		t.Errorf("no boundary → hard cut at limit, got %d", len([]rune(hard[0])))
	}
}

// --- typing manager -------------------------------------------------------------------

func TestTyping_StartStop(t *testing.T) {
	ft := &fakeTransport{}
	tm := newTypingManager(ft)
	tm.start("1")
	tm.start("1") // idempotent
	waitForCalls(t, ft, 1)
	tm.stop("1")
	tm.stop("1") // stopping twice is safe
	var nilTM *typingManager
	nilTM.start("1") // nil-safe
	nilTM.stop("1")
}

// --- config helpers -------------------------------------------------------------------

func TestConfigHelpers(t *testing.T) {
	if got := parseAllowList(" 1, 2 ,, 3 "); len(got) != 3 || got[0] != "1" || got[2] != "3" {
		t.Errorf("parseAllowList = %v", got)
	}
	t.Setenv("X_ENV_TEST", "v")
	if envOr("X_ENV_TEST", "def") != "v" || envOr("X_MISSING_ENV", "def") != "def" {
		t.Error("envOr")
	}
	e := &toolError{op: "send", detail: "boom"}
	if !strings.Contains(e.Error(), "boom") {
		t.Error("toolError.Error should include detail")
	}
}

func TestFetchBotUsername(t *testing.T) {
	ft := &fakeTransport{}
	// fakeTransport.cmd returns "ok message_id=1" which isn't JSON → empty username
	if fetchBotUsername(ft) != "" {
		t.Error("non-JSON bot info → empty username")
	}
}
