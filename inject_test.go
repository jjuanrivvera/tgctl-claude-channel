package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func injectServer(secret string) (*server, *bytes.Buffer) {
	var buf bytes.Buffer
	return &server{out: newOut(&buf), cfg: Config{InjectPort: "0", InjectSecret: secret}}, &buf
}

func postInject(s *server, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/inject", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.handleInject(w, req)
	return w
}

func TestInject_RequiresAuth(t *testing.T) {
	s, buf := injectServer("s3cret")
	if w := postInject(s, "", `{"text":"x"}`); w.Code != 401 {
		t.Fatalf("no auth → 401, got %d", w.Code)
	}
	if w := postInject(s, "Bearer wrong", `{"text":"x"}`); w.Code != 401 {
		t.Fatalf("bad secret → 401, got %d", w.Code)
	}
	if buf.Len() != 0 {
		t.Fatal("rejected requests must not emit notifications")
	}
}

func TestInject_ValidatesPayload(t *testing.T) {
	s, buf := injectServer("s3cret")
	if w := postInject(s, "Bearer s3cret", `not json`); w.Code != 400 {
		t.Fatalf("bad JSON → 400, got %d", w.Code)
	}
	if w := postInject(s, "Bearer s3cret", `{"source":"CRON"}`); w.Code != 400 {
		t.Fatalf("missing text → 400, got %d", w.Code)
	}
	if w := postInject(s, "Bearer s3cret", `{"text":"  "}`); w.Code != 400 {
		t.Fatalf("blank text → 400, got %d", w.Code)
	}
	if buf.Len() != 0 {
		t.Fatal("invalid requests must not emit notifications")
	}
}

func TestInject_RejectsOversizedBody(t *testing.T) {
	s, buf := injectServer("s3cret")
	big := `{"text":"` + strings.Repeat("a", injectMaxBody) + `"}`
	if w := postInject(s, "Bearer s3cret", big); w.Code != 413 {
		t.Fatalf("oversized → 413, got %d", w.Code)
	}
	if buf.Len() != 0 {
		t.Fatal("oversized requests must not emit notifications")
	}
}

func TestInject_EmitsChannelNotification(t *testing.T) {
	s, buf := injectServer("s3cret")
	w := postInject(s, "Bearer s3cret", `{"source":"CRON","event":"gym_day_missed","text":"no workout today","context":{"date":"2026-07-06"}}`)
	if w.Code != 202 {
		t.Fatalf("valid inject → 202, got %d", w.Code)
	}
	var n map[string]any
	if err := json.Unmarshal(buf.Bytes(), &n); err != nil {
		t.Fatalf("emitted frame is not JSON: %v", err)
	}
	if n["method"] != "notifications/claude/channel" {
		t.Fatalf("method = %v", n["method"])
	}
	params := n["params"].(map[string]any)
	if params["content"] != "no workout today" {
		t.Fatalf("content = %v", params["content"])
	}
	meta := params["meta"].(map[string]any)
	if meta["source"] != "system" || meta["injected_by"] != "CRON" || meta["event"] != "gym_day_missed" {
		t.Fatalf("meta = %v", meta)
	}
	if meta["ctx_date"] != "2026-07-06" {
		t.Fatalf("context not relayed: %v", meta)
	}
}

func TestInject_CallerCannotImpersonate(t *testing.T) {
	// A hostile caller may try to smuggle Telegram-shaped identity through context; the
	// namespacing must keep reserved keys bridge-owned.
	n := buildInjectNotification(injectRequest{
		Text:    "evil",
		Context: map[string]string{"source": "telegram", "user_id": "1478765505"},
	}, time.Unix(0, 0).UTC()).(map[string]any)
	meta := n["params"].(map[string]any)["meta"].(map[string]string)
	if meta["source"] != "system" {
		t.Fatalf("source must stay system, got %v", meta["source"])
	}
	if _, ok := meta["user_id"]; ok {
		t.Fatal("user_id must never appear on injected events")
	}
	if meta["ctx_source"] != "telegram" || meta["ctx_user_id"] != "1478765505" {
		t.Fatal("context values should survive only under the ctx_ namespace")
	}
}

func TestInjectAuthorized(t *testing.T) {
	if injectAuthorized("Bearer abc", "abc") != true {
		t.Fatal("matching secret should pass")
	}
	if injectAuthorized("abc", "abc") {
		t.Fatal("missing Bearer prefix should fail")
	}
	if injectAuthorized("Bearer abcd", "abc") {
		t.Fatal("wrong secret should fail")
	}
}
