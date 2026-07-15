package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTgctlEnv(t *testing.T) {
	t.Setenv("TGCTL_TOKEN", "sentinel") // register restore, then clear for the test
	if err := os.Unsetenv("TGCTL_TOKEN"); err != nil {
		t.Fatal(err)
	}

	for _, e := range tgctlEnv("") {
		if strings.HasPrefix(e, "TGCTL_TOKEN=") {
			t.Fatalf("empty token must not inject TGCTL_TOKEN, got %q", e)
		}
	}

	var found bool
	for _, e := range tgctlEnv("123:abc") {
		if e == "TGCTL_TOKEN=123:abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit token missing from subprocess env")
	}
}

// fakeTgctl writes an executable that prints the given auth-status JSON.
func fakeTgctl(t *testing.T, jsonOut string, exitCode int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tgctl")
	script := "#!/bin/sh\ncat <<'EOF'\n" + jsonOut + "\nEOF\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeyringAuthStatus(t *testing.T) {
	valid := fakeTgctl(t, `{"bot":"@x_bot","profile":"acue","valid":true}`, 0)
	st, err := keyringAuthStatus(context.Background(), Config{TgctlBin: valid})
	if err != nil {
		t.Fatalf("valid keyring auth rejected: %v", err)
	}
	if st.Bot != "@x_bot" || st.Profile != "acue" {
		t.Fatalf("unexpected status: %+v", st)
	}

	invalid := fakeTgctl(t, `{"bot":"@x_bot","profile":"acue","valid":false}`, 0)
	if _, err := keyringAuthStatus(context.Background(), Config{TgctlBin: invalid}); err == nil {
		t.Fatal("invalid stored credentials must be rejected")
	}

	failing := fakeTgctl(t, `{}`, 1)
	if _, err := keyringAuthStatus(context.Background(), Config{TgctlBin: failing}); err == nil {
		t.Fatal("tgctl failure must surface as an error")
	}
}
