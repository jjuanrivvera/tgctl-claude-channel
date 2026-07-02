package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPermRegexes(t *testing.T) {
	if !permReplyRe.MatchString("yes abcde") || !permReplyRe.MatchString("No ABCDE") {
		t.Error("valid permission text replies should match")
	}
	if permReplyRe.MatchString("hello world") || permReplyRe.MatchString("yes hello") { // 'hello' contains 'l'
		t.Error("non-permission text should not match")
	}
	if !permCallbackRe.MatchString("perm:allow:abcde") || permCallbackRe.MatchString("pick:a") {
		t.Error("permission callback data matching")
	}
}

func TestPermission_TextReply_Emits(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	m := &message{MessageID: 1, From: tgUser{ID: 1}}
	if !s.perms.handleTextReply(s, m, "1", "yes abcde") {
		t.Fatal("a permission text reply should be consumed")
	}
	if !strings.Contains(buf.String(), "notifications/claude/channel/permission") || !strings.Contains(buf.String(), "allow") {
		t.Fatalf("should emit an allow decision; got %q", buf.String())
	}
	if s.perms.handleTextReply(s, m, "1", "just a normal message") {
		t.Fatal("a normal message must not be treated as a permission reply")
	}
}

func TestPermission_Callback_AllowEmits(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	cq := &callbackQuery{ID: "c", Data: "perm:deny:abcde", From: tgUser{ID: 1}, Message: &message{MessageID: 2, Chat: tgChat{ID: 1}}}
	if !s.perms.handleCallback(s, cq) {
		t.Fatal("a permission callback should be consumed")
	}
	if !strings.Contains(buf.String(), "notifications/claude/channel/permission") || !strings.Contains(buf.String(), "deny") {
		t.Fatalf("should emit a deny decision; got %q", buf.String())
	}
	if !strings.Contains(ft.all(), "callback answer") {
		t.Error("the tap should be answered so Telegram stops spinning")
	}
}

func TestPermission_Callback_Unauthorized(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	cq := &callbackQuery{ID: "c", Data: "perm:allow:abcde", From: tgUser{ID: 999}, Message: &message{Chat: tgChat{ID: 1}}}
	if !s.perms.handleCallback(s, cq) {
		t.Fatal("a permission callback is still consumed even when unauthorized")
	}
	if buf.Len() != 0 {
		t.Fatal("an unauthorized tapper must not resolve a permission")
	}
	if !strings.Contains(ft.all(), "Not authorized") {
		t.Error("unauthorized tap should be told so")
	}
}

func TestPermission_NonPermissionCallback_NotConsumed(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	cq := &callbackQuery{ID: "c", Data: "app_choice_a", From: tgUser{ID: 1}}
	if s.perms.handleCallback(s, cq) {
		t.Fatal("a normal button tap must not be swallowed by the permission handler")
	}
}

func TestPermission_OnRequest_SendsButtons(t *testing.T) {
	s, ft, _ := newTestServer(t, "1", "2")
	s.perms.onRequest(s, json.RawMessage(`{"request_id":"abcde","tool_name":"Bash","description":"run","input_preview":"{}"}`))
	// goroutines fan out one sendMessage per allowlisted chat; wait for them.
	waitForCalls(t, ft, 2)
	got := ft.all()
	if !strings.Contains(got, "sendMessage") || !strings.Contains(got, "inline_keyboard") || !strings.Contains(got, "perm:allow:abcde") {
		t.Fatalf("permission request should fan out Allow/Deny buttons; got %q", got)
	}
}
