package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandler creates a fake command-handler executable implementing the list/run contract:
// `list` prints a manifest; `run ping <args>` echoes; `run boom` fails; `run quiet` is silent.
func writeHandler(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "handler")
	script := `#!/bin/sh
case "$1" in
  list) printf '%s' '[{"command":"Ping","description":"echo back"},{"command":"boom","description":"fails"},{"command":"","description":"skip"}]' ;;
  run)
    case "$2" in
      ping) printf 'pong: %s (chat=%s)' "$3" "$TG_CHAT_ID" ;;
      boom) printf 'kaboom' 1>&2; exit 1 ;;
      quiet) : ;;
    esac ;;
  callback) printf 'cb: %s (cbid=%s chat=%s)' "$2" "$TG_CALLBACK_ID" "$TG_CHAT_ID" ;;
esac
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCommandHook(t *testing.T) {
	if loadCommandHook("") != nil {
		t.Fatal("empty bin should yield nil hook")
	}
	if loadCommandHook("/nonexistent/handler-xyz") != nil {
		t.Fatal("missing bin should yield nil hook")
	}
	h := loadCommandHook(writeHandler(t))
	if h == nil {
		t.Fatal("valid handler should load")
	}
	if !h.handles("ping") || !h.handles("PING") || !h.handles("boom") {
		t.Fatalf("expected ping+boom handled: %v", h.byName)
	}
	if h.handles("nope") || len(h.commands) != 2 { // empty-named entry is skipped
		t.Fatalf("unexpected command set: %v", h.commands)
	}
}

func TestCommandHookNilSafe(t *testing.T) {
	var h *commandHook
	if h.handles("anything") {
		t.Fatal("nil hook must not claim commands")
	}
}

func TestCommandHookRunRelaysStdout(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	m := &message{MessageID: 5, Chat: tgChat{ID: 7, Type: "private"}, From: tgUser{ID: 7}}
	s.cmdHook.run(s, m, "7", "ping", "opus")
	if got := ft.all(); !strings.Contains(got, "pong: opus (chat=7)") {
		t.Fatalf("expected handler stdout relayed; calls:\n%s", got)
	}
}

func TestCommandHookRunReportsFailure(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	m := &message{MessageID: 5, Chat: tgChat{ID: 7, Type: "private"}, From: tgUser{ID: 7}}
	s.cmdHook.run(s, m, "7", "boom", "")
	if !strings.Contains(ft.all(), "kaboom") {
		t.Fatalf("expected failure detail relayed; got:\n%s", ft.all())
	}
}

func TestCommandHookRunSilent(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	m := &message{MessageID: 5, Chat: tgChat{ID: 7, Type: "private"}, From: tgUser{ID: 7}}
	s.cmdHook.run(s, m, "7", "quiet", "")
	if ft.count() != 0 {
		t.Fatalf("silent handler should send nothing; got:\n%s", ft.all())
	}
}

func TestCommandHookRegisterCommands(t *testing.T) {
	_, ft, _ := newTestServer(t, "7")
	h := loadCommandHook(writeHandler(t))
	h.registerCommands(ft)
	got := ft.all()
	if !strings.Contains(got, "setMyCommands") || !strings.Contains(got, "ping") || !strings.Contains(got, "start") {
		t.Fatalf("expected setMyCommands with ping+start; got:\n%s", got)
	}
}

func TestHandleMessageRoutesOperatorToHook(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleMessage(&message{MessageID: 1, Text: "/ping hi", Chat: tgChat{ID: 7, Type: "private"}, From: tgUser{ID: 7}})
	if !strings.Contains(ft.all(), "pong: hi") {
		t.Fatalf("operator hook command should run; got:\n%s", ft.all())
	}
}

func TestHandleMessageHookDeniesNonOperator(t *testing.T) {
	s, ft, _ := newTestServer(t, "7") // only 7 is allowed
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleMessage(&message{MessageID: 1, Text: "/ping hi", Chat: tgChat{ID: 9, Type: "private"}, From: tgUser{ID: 9}})
	if strings.Contains(ft.all(), "pong") {
		t.Fatalf("non-operator must not run a privileged hook command; got:\n%s", ft.all())
	}
}

func TestHandleMessageUnknownCommandFallsThrough(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleMessage(&message{MessageID: 1, Text: "/help", Chat: tgChat{ID: 7, Type: "private"}, From: tgUser{ID: 7}})
	if !strings.Contains(ft.all(), "send 7") { // handleCommand answers /help
		t.Fatalf("unknown command should fall through to handleCommand; got:\n%s", ft.all())
	}
}

func TestOwnsCallback(t *testing.T) {
	var nilh *commandHook
	if nilh.ownsCallback("hnd:x") {
		t.Fatal("nil hook owns nothing")
	}
	h := loadCommandHook(writeHandler(t))
	if !h.ownsCallback("hnd:model:opus") {
		t.Fatal("should own hnd: prefix")
	}
	if h.ownsCallback("pick_a") {
		t.Fatal("should not own a non-prefixed callback")
	}
}

func TestCommandHookCallbackRelaysOutput(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	cq := &callbackQuery{ID: "cb1", Data: "hnd:model:sonnet", From: tgUser{ID: 7}, Message: &message{MessageID: 9, Chat: tgChat{ID: 7}}}
	s.cmdHook.callback(s, cq)
	if !strings.Contains(ft.all(), "cb: hnd:model:sonnet (cbid=cb1 chat=7)") {
		t.Fatalf("callback handler output should be relayed with env; got:\n%s", ft.all())
	}
}

func TestHandleCallbackRoutesOperatorToHandler(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleCallback(&callbackQuery{ID: "cb1", Data: "hnd:effort:high", From: tgUser{ID: 7}, Message: &message{MessageID: 9, Chat: tgChat{ID: 7}}})
	if !strings.Contains(ft.all(), "cb: hnd:effort:high") {
		t.Fatalf("operator hnd callback should route to handler; got:\n%s", ft.all())
	}
}

func TestHandleCallbackDeniesNonOperator(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleCallback(&callbackQuery{ID: "cb1", Data: "hnd:model:opus", From: tgUser{ID: 9}, Message: &message{MessageID: 9, Chat: tgChat{ID: 9}}})
	got := ft.all()
	if strings.Contains(got, "cb: hnd") {
		t.Fatalf("non-operator must not run a handler callback; got:\n%s", got)
	}
	if !strings.Contains(got, "Not authorized") {
		t.Fatalf("non-operator tap should be answered Not authorized; got:\n%s", got)
	}
}

func TestHandleCallbackNonHandlerFallsThrough(t *testing.T) {
	s, ft, _ := newTestServer(t, "7")
	s.cmdHook = loadCommandHook(writeHandler(t))
	s.handleCallback(&callbackQuery{ID: "cb1", Data: "pick_a", From: tgUser{ID: 7}, Message: &message{MessageID: 9, Chat: tgChat{ID: 7}}})
	if strings.Contains(ft.all(), "cb: ") {
		t.Fatalf("a non-hnd callback must not reach the handler; got:\n%s", ft.all())
	}
}

func TestCommandArgs(t *testing.T) {
	cases := map[string]string{
		"/model opus":         "opus",
		"/model@bot opus 4.8": "opus 4.8",
		"/clear":              "",
		"/goal  do the thing": "do the thing",
	}
	for in, want := range cases {
		if got := commandArgs(in); got != want {
			t.Errorf("commandArgs(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if truncateRunes("hello", 10) != "hello" {
		t.Fatal("no truncation under limit")
	}
	if got := truncateRunes("héllo", 3); got != "hél" {
		t.Fatalf("rune-aware truncation; got %q", got)
	}
}
