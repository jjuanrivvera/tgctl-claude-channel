package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
)

// fakeToken has a real token's SHAPE (numeric bot id, long hash) and is obviously not one.
// Never put a live token in a test, a fixture or a bug report.
const fakeToken = "123456789:AAFfakeFAKEfakeFAKEfakeFAKEfake-01"

// leakyTransport reproduces what an unpatched tgctl wrote to stderr when a request died at
// the transport level: Go's *url.Error, printing the API URL with the token in its path.
type leakyTransport struct{ token string }

func (l leakyTransport) fail(op string) (string, error) {
	return "", &toolError{
		op: op,
		detail: `Error: sendMessage: Post "https://api.telegram.org/bot` + l.token +
			`/sendMessage": read tcp [2001:db8::1]:443: read: connection timed out`,
		err: errors.New("exit status 1"),
	}
}

func (l leakyTransport) send(string, string) (string, error) { return l.fail("message") }
func (l leakyTransport) react(string, string, string) (string, error) {
	return l.fail("api")
}
func (l leakyTransport) edit(string, string, string) (string, error) { return l.fail("api") }
func (l leakyTransport) action(string, string) (string, error)       { return l.fail("api") }
func (l leakyTransport) cmd(args ...string) (string, error)          { return l.fail(args[0]) }

func newLeakyServer(t *testing.T, chatID string) (*server, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	tg := leakyTransport{token: fakeToken}
	buf := &bytes.Buffer{}
	return &server{
		out:    newOut(buf),
		tg:     tg,
		typing: newTypingManager(tg),
		store:  newAccessStore(dir, []string{chatID}, "", false),
		perms:  newPermissionManager(),
		cfg:    Config{TgctlBin: "tgctl", StateDir: dir},
	}, buf
}

func withLiveToken(t *testing.T, tok string) {
	t.Helper()
	setLiveToken(tok)
	t.Cleanup(func() { setLiveToken("") })
}

// The regression test for jjuanrivvera/tgctl#21 at this end: whatever tgctl hands back, the
// frame that reaches the agent — and its transcript on disk — must not contain the token.
func TestToolResult_NeverCarriesTheToken(t *testing.T) {
	withLiveToken(t, fakeToken)
	s, buf := newLeakyServer(t, "42")

	s.handleToolCall(inMsg{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"reply","arguments":{"chat_id":"42","text":"hi"}}`),
	})

	frame := buf.String()
	if strings.Contains(frame, fakeToken) {
		t.Fatalf("the bot token reached the MCP result frame: %s", frame)
	}
	if strings.Contains(frame, "AAFfakeFAKE") {
		t.Fatalf("the token hash reached the MCP result frame: %s", frame)
	}

	// The result must still be a usable error: redaction may not cost the agent the reason
	// the call failed, nor which bot it was.
	var got struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("frame is not valid JSON-RPC: %v (%s)", err, frame)
	}
	if !got.Result.IsError || len(got.Result.Content) == 0 {
		t.Fatalf("expected an isError tool result, got %s", frame)
	}
	text := got.Result.Content[0].Text
	for _, want := range []string{"123456789:<redacted>", "connection timed out", "sendMessage"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text lost %q: %s", want, text)
		}
	}
}

// A protocol-level error frame goes out through the same choke point.
func TestRPCError_NeverCarriesTheToken(t *testing.T) {
	withLiveToken(t, fakeToken)
	buf := &bytes.Buffer{}
	newOut(buf).send(rpcErr(json.RawMessage(`1`), -32602, "invalid params near "+fakeToken))

	if strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("token in an rpc error frame: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "123456789:<redacted>") {
		t.Errorf("expected the redacted bot id, got %s", buf.String())
	}
}

// Ordinary frames must survive untouched — redaction is not allowed to mangle content.
func TestSend_LeavesOrdinaryFramesIntact(t *testing.T) {
	withLiveToken(t, fakeToken)
	buf := &bytes.Buffer{}
	newOut(buf).send(result(json.RawMessage(`7`), map[string]any{"ok": true, "chat_id": "1478765505", "at": "09:30"}))

	var got struct {
		ID     int `json:"id"`
		Result struct {
			OK     bool   `json:"ok"`
			ChatID string `json:"chat_id"`
			At     string `json:"at"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v (%s)", err, buf.String())
	}
	if got.ID != 7 || !got.Result.OK || got.Result.ChatID != "1478765505" || got.Result.At != "09:30" {
		t.Errorf("frame altered: %s", buf.String())
	}
}

func TestRedactSecrets(t *testing.T) {
	withLiveToken(t, fakeToken)
	cases := []struct{ name, in, want string }{
		{"method URL", "Post \"https://api.telegram.org/bot" + fakeToken + "/sendMessage\": EOF",
			"Post \"https://api.telegram.org/bot123456789:<redacted>/sendMessage\": EOF"},
		{"file download URL", "https://api.telegram.org/file/bot" + fakeToken + "/photos/f.jpg",
			"https://api.telegram.org/file/bot123456789:<redacted>/photos/f.jpg"},
		{"bare token", "TGCTL_TOKEN=" + fakeToken, "TGCTL_TOKEN=123456789:<redacted>"},
		{"unrelated ids untouched", "chat_id:1478765505 at 09:30", "chat_id:1478765505 at 09:30"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSecrets(tc.in); got != tc.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A token the patterns would not recognize (a self-hosted Bot API server's, say) is still
// masked, because the process knows its own credential.
func TestRedactSecrets_KnownTokenOfUnusualShape(t *testing.T) {
	withLiveToken(t, "42:short+odd/shape==")
	got := redactSecrets("Post \"http://localhost:8081/bot42:short+odd/shape==/getMe\": EOF")
	if strings.Contains(got, "short+odd") {
		t.Errorf("known token not masked: %s", got)
	}
	if !strings.Contains(got, "42:<redacted>") {
		t.Errorf("expected the redacted bot id, got %s", got)
	}
}

// Anything merely logged on the host is scrubbed too.
func TestRedactingWriter_ScrubsTheLog(t *testing.T) {
	withLiveToken(t, fakeToken)
	buf := &bytes.Buffer{}
	lg := log.New(redactingWriter{buf}, "", 0)

	lg.Printf("inbound stopped: getUpdates: Get %q: i/o timeout",
		"https://api.telegram.org/bot"+fakeToken+"/getUpdates")

	if strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("token reached the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "123456789:<redacted>") {
		t.Errorf("expected the redacted bot id, got %s", buf.String())
	}
}
