package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHandleArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{name: "no arguments"},
		{name: "long version", args: []string{"--version"}},
		{name: "short version", args: []string{"-v"}},
		{name: "version command", args: []string{"version"}},
		{name: "long help", args: []string{"--help"}},
		{name: "short help", args: []string{"-h"}},
		{name: "unknown flag", args: []string{"--bogus"}, code: 2},
		{name: "unknown command", args: []string{"bogus"}, code: 2},
		{name: "extra argument", args: []string{"--version", "bogus"}, code: 2},
		{name: "extra help argument", args: []string{"--help", "bogus"}, code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			handled, code := handleArgs(tc.args, &stdout, &stderr)
			if handled != (len(tc.args) > 0) || code != tc.code {
				t.Fatalf("handled=%v code=%d, want handled=%v code=%d", handled, code, len(tc.args) > 0, tc.code)
			}
			wantOut, wantErr := "", ""
			if tc.code == 2 {
				wantErr = usage
			} else if len(tc.args) > 0 {
				wantOut = "tgctl-claude-channel " + version + "\n"
				if tc.args[0] == "--help" || tc.args[0] == "-h" {
					wantOut = usage
				}
			}
			if stdout.String() != wantOut || stderr.String() != wantErr {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q stderr=%q", stdout.String(), stderr.String(), wantOut, wantErr)
			}
		})
	}
}

func TestMain_ArgumentsDoNotStartChannel(t *testing.T) {
	for _, arg := range []string{"--version", "-v", "version", "--help", "-h", "--bogus", "bogus"} {
		t.Run(arg, func(t *testing.T) {
			dir := t.TempDir()
			stateDir := filepath.Join(dir, "state")
			t.Setenv("TGCTL_CHANNEL_TEST_MAIN", "1")
			t.Setenv("TGCTL_CHANNEL_STATE_DIR", stateDir)
			t.Setenv("TGCTL_TOKEN", "")
			t.Setenv("TGCTL_BIN", filepath.Join(dir, "missing-tgctl"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChannelMainHelper$", "--", arg)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			wantCode := 0
			if arg == "--bogus" || arg == "bogus" {
				wantCode = 2
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantCode {
				t.Fatalf("main(%s): %v; stdout=%q stderr=%q", arg, err, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("argument handling touched the state directory: %v", err)
			}
			if wantCode == 0 && (stdout.Len() == 0 || stderr.Len() > 0) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if wantCode == 2 && (stdout.Len() > 0 || !strings.Contains(stderr.String(), "Usage:")) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestChannelMainHelper(t *testing.T) {
	if os.Getenv("TGCTL_CHANNEL_TEST_MAIN") != "1" {
		return
	}
	os.Args = append([]string{"tgctl-claude-channel"}, os.Args[3:]...)
	main()
}

func TestMain_StdinShutdown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		reason string
	}{
		{"EOF", "", "stdin EOF from Claude Code"},
		{"read error", "{bad}", "stdin read error from Claude Code:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TGCTL_CHANNEL_TEST_MAIN", "1")
			t.Setenv("TGCTL_CHANNEL_STATE_DIR", dir)
			t.Setenv("TGCTL_TOKEN", "111:secret")
			t.Setenv("TGCTL_BIN", fakeTgctl(t, `{}`, 0))
			t.Setenv("TGCTL_CHANNEL_COMMAND_HANDLER", "")
			t.Setenv("TGCTL_CHANNEL_INJECT_PORT", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChannelMainHelper$", "--")
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "shutting down: "+tc.reason) {
				t.Fatalf("shutdown: %v, output=%s", err, out)
			}
			if _, err := os.Stat(resolvePIDFile(dir, "111")); !os.IsNotExist(err) {
				t.Fatalf("shutdown left its PID file: %v", err)
			}
		})
	}
}

func TestMain_SignalShutdown(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  os.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGINT", syscall.SIGINT},
		{"SIGHUP", syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TGCTL_CHANNEL_TEST_MAIN", "1")
			t.Setenv("TGCTL_CHANNEL_STATE_DIR", dir)
			t.Setenv("TGCTL_TOKEN", "111:secret")
			t.Setenv("TGCTL_BIN", fakeTgctl(t, `{}`, 0))
			t.Setenv("TGCTL_CHANNEL_COMMAND_HANDLER", "")
			t.Setenv("TGCTL_CHANNEL_INJECT_PORT", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChannelMainHelper$", "--")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close() }()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			if _, err := stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")); err != nil {
				t.Fatal(err)
			}
			// A reply proves startup has installed the handlers before we signal our child.
			line, err := bufio.NewReader(stdout).ReadBytes('\n')
			if err != nil || !json.Valid(line) {
				t.Fatalf("startup handshake: %v, reply=%q", err, line)
			}
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("signal shutdown: %v, stderr=%s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "shutting down: signal received: "+tc.name) {
				t.Fatalf("signal missing from shutdown log: %s", stderr.String())
			}
			if _, err := os.Stat(resolvePIDFile(dir, "111")); !os.IsNotExist(err) {
				t.Fatalf("shutdown left its PID file: %v", err)
			}
		})
	}
}
