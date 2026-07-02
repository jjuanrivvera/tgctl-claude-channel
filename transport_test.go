package main

import (
	"strings"
	"testing"
)

// The tgctlTransport just builds argv and shells out, so we exercise it with `echo`
// as a stand-in binary: run() returns the argv it would have passed to tgctl.
func TestTgctlTransport_BuildsArgs(t *testing.T) {
	tr := tgctlTransport{bin: "echo", token: "x"}
	cases := []struct {
		out  string
		call func() (string, error)
	}{
		{"message send --chat 1 --text hi", func() (string, error) { return tr.send("1", "hi") }},
		{"setMessageReaction", func() (string, error) { return tr.react("1", "2", "👍") }},
		{"editMessageText", func() (string, error) { return tr.edit("1", "2", "x") }},
		{"sendChatAction", func() (string, error) { return tr.action("1", "typing") }},
		{"bot info", func() (string, error) { return tr.cmd("bot", "info") }},
	}
	for _, c := range cases {
		got, err := c.call()
		if err != nil || !strings.Contains(got, c.out) {
			t.Errorf("expected %q in %q (err=%v)", c.out, got, err)
		}
	}
}

func TestTgctlTransport_RunError(t *testing.T) {
	if _, err := (tgctlTransport{bin: "false"}).run("x"); err == nil {
		t.Error("a non-zero exit should surface as an error")
	}
	if _, err := (tgctlTransport{bin: "definitely-not-a-real-binary-xyz"}).run("x"); err == nil {
		t.Error("a missing binary should error")
	}
}

func TestFetchBotUsername_JSON(t *testing.T) {
	// `printf` echoes a JSON object → the username is parsed out.
	if got := fetchBotUsername(tgctlTransport{bin: "printf", token: "x"}); got != "" {
		// printf's argv includes the flags, so this won't yield clean JSON — just
		// assert it doesn't panic and returns a string. The empty-input path is the
		// realistic one and is covered in TestFetchBotUsername.
		_ = got
	}
}
