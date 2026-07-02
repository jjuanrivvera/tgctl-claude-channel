package main

import (
	"context"
	"strings"
	"testing"
)

func TestHandleUpdate_Routes(t *testing.T) {
	s, _, buf := newTestServer(t, "1")
	s.handleUpdate(update{Message: &message{MessageID: 1, Text: "hi", Chat: tgChat{ID: 1, Type: "private"}, From: tgUser{ID: 1}}})
	if !strings.Contains(buf.String(), "hi") {
		t.Error("a message update should be handled")
	}
	buf.Reset()
	s.handleUpdate(update{CallbackQuery: &callbackQuery{ID: "c", Data: "x", From: tgUser{ID: 1}, Message: &message{Chat: tgChat{ID: 1}}}})
	if !strings.Contains(buf.String(), "callback_query") {
		t.Error("a callback update should be handled")
	}
	s.handleUpdate(update{}) // empty update: no panic
}

func TestReplyToBotUsername(t *testing.T) {
	if (&message{}).replyToBotUsername() != "" {
		t.Error("no reply → empty")
	}
	m := &message{ReplyTo: &message{From: tgUser{Username: "bot"}}}
	if m.replyToBotUsername() != "bot" {
		t.Error("reply-to should surface the replied-to username")
	}
}

// getUpdates/latestOffset/deleteWebhook shell out; `echo` stands in for tgctl and
// returns non-JSON, so we exercise the exec + error/parse paths without a real bot.
func TestExecPaths_WithEcho(t *testing.T) {
	ctx := context.Background()
	cfg := Config{TgctlBin: "echo"}
	if _, err := getUpdates(ctx, cfg, 5); err == nil {
		t.Error("non-JSON getUpdates output should error")
	}
	if latestOffset(ctx, cfg) != 0 {
		t.Error("non-JSON latestOffset → 0")
	}
	deleteWebhook(ctx, cfg) // best-effort, must not panic
}

func TestRunInbound_CancelledReturns(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	s.cfg.TgctlBin = "echo"
	s.cfg.OffsetFile = t.TempDir() + "/off"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.runInbound(ctx); err == nil {
		t.Error("a cancelled context should stop the poller with an error")
	}
}
