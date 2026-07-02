package main

import (
	"strings"
	"testing"
)

func TestParseUpdates(t *testing.T) {
	if ups, err := parseUpdates([]byte("")); err != nil || ups != nil {
		t.Errorf("empty → nil, got %v %v", ups, err)
	}
	if ups, err := parseUpdates([]byte("null")); err != nil || ups != nil {
		t.Errorf("null → nil, got %v %v", ups, err)
	}
	ups, err := parseUpdates([]byte(`[{"update_id":3,"message":{"message_id":1,"text":"hi","chat":{"id":9,"type":"private"},"from":{"id":9}}}]`))
	if err != nil || len(ups) != 1 || ups[0].UpdateID != 3 || ups[0].Message.Text != "hi" {
		t.Fatalf("parse failed: %v %v", ups, err)
	}
}

func TestOffsetRoundtrip(t *testing.T) {
	p := t.TempDir() + "/off"
	saveOffset(p, 12345)
	if loadOffset(p) != 12345 {
		t.Fatal("offset should persist")
	}
	if loadOffset(t.TempDir()+"/missing") != 0 {
		t.Fatal("missing offset → 0")
	}
}

func TestBuildMessageNotification_Meta(t *testing.T) {
	m := &message{MessageID: 7, Date: 0, Chat: tgChat{ID: 3, Type: "private"}, From: tgUser{ID: 5, Username: "juan"}}
	n := buildMessageNotification(m, "3", "5", "hello", "/inbox/x.jpg", &attachment{Kind: "document", FileID: "F", Size: 10, Mime: "application/pdf", Name: "a.pdf"}).(map[string]any)
	if n["method"] != "notifications/claude/channel" {
		t.Fatalf("method = %v", n["method"])
	}
	params := n["params"].(map[string]any)
	if params["content"] != "hello" {
		t.Fatalf("content = %v", params["content"])
	}
	meta := params["meta"].(map[string]string)
	for k, want := range map[string]string{
		"chat_id": "3", "user_id": "5", "message_id": "7", "user": "@juan",
		"image_path": "/inbox/x.jpg", "attachment_kind": "document", "attachment_file_id": "F",
		"attachment_name": "a.pdf", "attachment_mime": "application/pdf",
	} {
		if meta[k] != want {
			t.Errorf("meta[%s] = %q, want %q", k, meta[k], want)
		}
	}
	if meta["ts"] == "" {
		t.Error("meta.ts should be set")
	}
}

func TestBuildCallbackNotification(t *testing.T) {
	cq := &callbackQuery{ID: "c1", Data: "pick_a", From: tgUser{ID: 5}, Message: &message{MessageID: 2, Chat: tgChat{ID: 3}}}
	n := buildCallbackNotification(cq).(map[string]any)
	meta := n["params"].(map[string]any)["meta"].(map[string]string)
	if meta["kind"] != "callback_query" || meta["callback_query_id"] != "c1" || meta["data"] != "pick_a" || meta["chat_id"] != "3" {
		t.Fatalf("callback meta wrong: %v", meta)
	}
}

func TestDefaultCaption(t *testing.T) {
	cases := []struct {
		m    message
		want string
	}{
		{message{Text: "hi"}, "hi"},
		{message{Caption: "cap"}, "cap"},
		{message{Photo: []fileRef{{FileID: "p"}}}, "(photo)"},
		{message{Document: &fileRef{FileName: "a.zip"}}, "(document: a.zip)"},
		{message{Voice: &fileRef{}}, "(voice message)"},
		{message{Video: &fileRef{}}, "(video)"},
		{message{VideoNote: &fileRef{}}, "(video note)"},
		{message{Sticker: &sticker{Emoji: "🎉"}}, "(sticker 🎉)"},
	}
	for _, c := range cases {
		if got := c.m.defaultCaption(); got != c.want {
			t.Errorf("defaultCaption(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestMessageHelpers(t *testing.T) {
	if (&message{Text: "a"}).textOrCaption() != "a" || (&message{Caption: "b"}).textOrCaption() != "b" {
		t.Error("textOrCaption")
	}
	if (tgUser{Username: "u"}).displayName() != "@u" || (tgUser{FirstName: "F"}).displayName() != "F" || (tgUser{ID: 9}).displayName() != "9" {
		t.Error("displayName")
	}
	if !isCommand("/start") || isCommand("hi") {
		t.Error("isCommand")
	}
	if commandName("/start@bot arg") != "start" || commandName("/STATUS") != "status" {
		t.Error("commandName")
	}
}

// --- integration: handleMessage / handleCallback --------------------------------------

func TestHandle_DeliversAllowlisted(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	s.handleMessage(&message{MessageID: 1, Text: "hola", Chat: tgChat{ID: 1, Type: "private"}, From: tgUser{ID: 1}})
	if !strings.Contains(buf.String(), "notifications/claude/channel") || !strings.Contains(buf.String(), "hola") {
		t.Fatalf("allowlisted message should be delivered as a turn; got %q", buf.String())
	}
}

func TestHandle_DropsNonAllowlisted(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	s.handleMessage(&message{MessageID: 1, Text: "intruder", Chat: tgChat{ID: 2, Type: "private"}, From: tgUser{ID: 2}})
	if buf.Len() != 0 {
		t.Fatalf("non-allowlisted DM under allowlist policy must not deliver; got %q", buf.String())
	}
}

func TestHandle_CommandNotRelayed(t *testing.T) {
	s, ft, buf := newTestServer(t, "1")
	s.handleMessage(&message{MessageID: 1, Text: "/start", Chat: tgChat{ID: 1, Type: "private"}, From: tgUser{ID: 1}})
	if buf.Len() != 0 {
		t.Fatal("a command must not be relayed to the session")
	}
	if !strings.Contains(ft.all(), "send") {
		t.Fatal("/start should reply to the user")
	}
}

func TestHandle_CallbackDelivered(t *testing.T) {
	s, _, buf := newTestServer(t, "5")
	s.handleCallback(&callbackQuery{ID: "c", Data: "x", From: tgUser{ID: 5}, Message: &message{Chat: tgChat{ID: 5}}})
	if !strings.Contains(buf.String(), "callback_query") {
		t.Fatalf("allowlisted tap should be delivered; got %q", buf.String())
	}
}
