package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// resolvePIDFile keys the PID file by bot identity (see pidIdentity), falling back to
// the legacy shared bot.pid only when no identity could be established.
func resolvePIDFile(stateDir, id string) string {
	if id != "" {
		return filepath.Join(stateDir, "bot-"+id+".pid")
	}
	return filepath.Join(stateDir, "bot.pid")
}

// pidIdentity names the bot this instance polls: the numeric id from the token, or under
// keyring auth (no token in the environment) the bot_id `tgctl auth status` reported. The
// Hub runs on the keyring, so without this it would lose the one-consumer guarantee. The
// display name is not used: it is not unique, and the id keeps both modes on one file name.
func pidIdentity(token, keyringBotID string) string {
	if id := botIDFromToken(token); id != "" {
		return id
	}
	if _, err := strconv.ParseUint(keyringBotID, 10, 64); err != nil {
		return "" // never let an unexpected value become a path component
	}
	return keyringBotID
}

// Only another consumer of the same bot can cause a getUpdates 409. A shared state
// directory alone is never evidence that a running poller should be terminated (#8).
func writePID(stateDir, id string) {
	writePIDWith(stateDir, id, signalPID, processBotID)
}

func writePIDWith(stateDir, id string, signalProcess func(int, os.Signal) error, botIDForPID func(int) string) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Printf("create PID directory: %v", err)
		return
	}
	pidFile := resolvePIDFile(stateDir, id)
	legacy := filepath.Join(stateDir, "bot.pid")
	if id != "" {
		seen := make(map[int]bool)
		for _, path := range []string{pidFile, legacy} {
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			previous, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || previous <= 1 || previous == os.Getpid() || seen[previous] {
				continue
			}
			// Old bot.pid files carry no identity. Fail closed when the old process's
			// token is unavailable (keyring auth, missing /proc, or denied access).
			if path == legacy && botIDForPID(previous) != id {
				continue
			}
			seen[previous] = true
			if err := signalProcess(previous, syscall.Signal(0)); err != nil {
				// EPERM means it is alive but owned by someone else: it still holds
				// getUpdates, and the operator needs to hear that rather than a 409 later.
				if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
					log.Printf("poller pid=%d for bot=%s is alive but cannot be replaced: %v", previous, id, err)
				}
				continue
			}
			log.Printf("replacing poller pid=%d for bot=%s: same bot starting, only one getUpdates consumer is allowed", previous, id)
			if err := signalProcess(previous, syscall.SIGTERM); err != nil {
				log.Printf("terminate poller pid=%d: %v", previous, err)
				continue
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		log.Printf("write PID file: %v", err)
	}
}

func signalPID(pid int, sig os.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = proc.Release() }()
	return proc.Signal(sig)
}

func processBotID(pid int) string {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	for _, entry := range strings.Split(string(b), "\x00") {
		if token, ok := strings.CutPrefix(entry, "TGCTL_TOKEN="); ok {
			return botIDFromToken(token)
		}
	}
	return ""
}

func removePID(stateDir, id string) {
	pidFile := resolvePIDFile(stateDir, id)
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid == os.Getpid() {
			if err := os.Remove(pidFile); err != nil {
				log.Printf("remove PID file: %v", err)
			}
		}
	}
}
