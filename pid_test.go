package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolvePIDFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		token string
		name  string
	}{
		{"111:secretA", "bot-111.pid"},
		{"222:secretB", "bot-222.pid"},
		{" 111:rotated ", "bot-111.pid"},
		{"", "bot.pid"},
		{"malformed", "bot.pid"},
	} {
		if got := resolvePIDFile(dir, botIDFromToken(tc.token)); got != filepath.Join(dir, tc.name) {
			t.Errorf("resolvePIDFile(%q) = %q, want %s", tc.token, got, tc.name)
		}
	}
}

func TestWritePID_DifferentBot(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "bot-222.pid")
	legacy := filepath.Join(dir, "bot.pid")
	for _, path := range []string{other, legacy} {
		if err := os.WriteFile(path, []byte("424242"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePIDWith(dir, "111", func(pid int, sig os.Signal) error {
		t.Fatalf("must not signal bot 222: pid=%d signal=%v", pid, sig)
		return nil
	}, func(int) string { return "222" })
	for _, path := range []string{other, legacy} {
		if b, err := os.ReadFile(path); err != nil || string(b) != "424242" {
			t.Fatalf("other bot's PID file changed: %q, %v", b, err)
		}
	}
	if b, err := os.ReadFile(resolvePIDFile(dir, "111")); err != nil || string(b) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("own PID not recorded: %q, %v", b, err)
	}
}

func TestWritePID_Replacement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		perBot    string
		legacy    string
		legacyBot string
		probeErr  bool
		termErr   bool
		wantTerm  bool
	}{
		{name: "same bot", token: "111:secret", perBot: "424242", wantTerm: true},
		{name: "verified legacy", token: "111:secret", legacy: "424242", legacyBot: "111", wantTerm: true},
		{name: "duplicate PID", token: "111:secret", perBot: "424242", legacy: "424242", legacyBot: "111", wantTerm: true},
		{name: "unknown legacy", token: "111:secret", legacy: "424242"},
		{name: "keyring auth", legacy: "424242", legacyBot: "111"},
		{name: "malformed token", token: "bad", legacy: "424242", legacyBot: "111"},
		{name: "invalid PID", token: "111:secret", perBot: "bad"},
		{name: "init PID", token: "111:secret", perBot: "1"},
		{name: "negative PID", token: "111:secret", perBot: "-2"},
		{name: "own PID", token: "111:secret", perBot: strconv.Itoa(os.Getpid())},
		{name: "exited process", token: "111:secret", perBot: "424242", probeErr: true},
		{name: "signal failure", token: "111:secret", perBot: "424242", termErr: true, wantTerm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{"bot-111.pid": tc.perBot, "bot.pid": tc.legacy} {
				if content != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var terms int
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(redactingWriter{&logs})
			t.Cleanup(func() { log.SetOutput(oldLog) })
			writePIDWith(dir, botIDFromToken(tc.token), func(pid int, sig os.Signal) error {
				if pid != 424242 {
					t.Fatalf("unexpected PID %d", pid)
				}
				if sig == syscall.Signal(0) {
					if tc.probeErr {
						return errors.New("process not running")
					}
					return nil
				}
				if sig != syscall.SIGTERM {
					t.Fatalf("unexpected signal %v", sig)
				}
				terms++
				if tc.termErr {
					return errors.New("permission denied")
				}
				return nil
			}, func(int) string { return tc.legacyBot })
			wantTerms := 0
			if tc.wantTerm {
				wantTerms = 1
			}
			if terms != wantTerms {
				t.Fatalf("SIGTERM calls = %d, want %d", terms, wantTerms)
			}
			if tc.wantTerm && !strings.Contains(logs.String(), "replacing poller pid=424242 for bot=111: same bot starting") {
				t.Fatalf("missing replacement reason: %s", logs.String())
			}
			if b, err := os.ReadFile(resolvePIDFile(dir, botIDFromToken(tc.token))); err != nil || string(b) != strconv.Itoa(os.Getpid()) {
				t.Fatalf("own PID not recorded: %q, %v", b, err)
			}
		})
	}
}

func TestRemovePID_OnlyOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		removed bool
	}{
		{"owned", strconv.Itoa(os.Getpid()), true},
		{"replaced", "424242", false},
		{"invalid", "bad", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			own := resolvePIDFile(dir, "111")
			other := resolvePIDFile(dir, "222")
			legacy := resolvePIDFile(dir, "")
			for _, path := range []string{own, other, legacy} {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			removePID(dir, "111")
			_, err := os.Stat(own)
			if os.IsNotExist(err) != tc.removed {
				t.Fatalf("own PID file removed = %v, want %v (err=%v)", os.IsNotExist(err), tc.removed, err)
			}
			for _, path := range []string{other, legacy} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("removed another bot's PID file: %v", err)
				}
			}
		})
	}
}

func TestProcessBotID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("legacy process identity uses Linux /proc; unavailable identity is left alone")
	}
	for _, token := range []string{"111:secret", "", "malformed"} {
		t.Run(token, func(t *testing.T) {
			t.Setenv("TGCTL_TOKEN", token)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'ready\\n'; read -r input")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close() }()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			// /proc can expose an empty environment while the child is still in exec.
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("process readiness: %v, output=%q", err, line)
			}
			if got := processBotID(cmd.Process.Pid); got != botIDFromToken(token) {
				t.Fatalf("processBotID = %q, want %q", got, botIDFromToken(token))
			}
		})
	}
	if got := processBotID(-1); got != "" {
		t.Fatalf("missing process must have unknown identity, got %q", got)
	}
}

func TestPIDIdentity(t *testing.T) {
	for _, tc := range []struct{ token, keyringBot, want string }{
		{"111:secret", "", "111"},
		{"111:secret", "@other_bot", "111"}, // a token always wins over the keyring
		{"", "@jjuanrivvera_bot", "kr-jjuanrivvera_bot"},
		{"", "hub", "kr-hub"},
		{"", "../../etc", "kr-etc"}, // never a path component
		{"", "", ""},
	} {
		if got := pidIdentity(tc.token, tc.keyringBot); got != tc.want {
			t.Errorf("pidIdentity(%q, %q) = %q, want %q", tc.token, tc.keyringBot, got, tc.want)
		}
	}
}
