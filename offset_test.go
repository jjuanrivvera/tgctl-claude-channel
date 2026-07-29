package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBotIDFromToken(t *testing.T) {
	cases := map[string]string{
		"123456789:AAExampleSecretValue": "123456789",
		"  987654321:xyz  ":              "987654321",
		"":                               "",
		"nocolon":                        "",
		":onlysecret":                    "",
		"abc:secret":                     "", // non-numeric id → treat as unknown
	}
	for token, want := range cases {
		if got := botIDFromToken(token); got != want {
			t.Errorf("botIDFromToken(%q) = %q, want %q", token, got, want)
		}
	}
}

func TestResolveOffsetFile_PerBotDefault(t *testing.T) {
	dir := t.TempDir()
	path, legacy := resolveOffsetFile(dir, "", "123456789:secret")
	want := filepath.Join(dir, "poll-offset-123456789")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if legacy != filepath.Join(dir, "poll-offset") {
		t.Errorf("legacy = %q, want the shared poll-offset path", legacy)
	}
}

func TestResolveOffsetFile_TwoBotsDiffer(t *testing.T) {
	dir := t.TempDir()
	a, _ := resolveOffsetFile(dir, "", "111:secretA")
	b, _ := resolveOffsetFile(dir, "", "222:secretB")
	if a == b {
		t.Fatalf("different bot ids must derive different offset files, both = %q", a)
	}
}

func TestResolveOffsetFile_EnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "custom-offset")
	path, legacy := resolveOffsetFile(dir, override, "123456789:secret")
	if path != override {
		t.Errorf("explicit override must win: path = %q, want %q", path, override)
	}
	if legacy != "" {
		t.Errorf("no migration for explicit override, got legacy = %q", legacy)
	}
}

func TestResolveOffsetFile_NoTokenKeepsLegacyName(t *testing.T) {
	dir := t.TempDir()
	path, legacy := resolveOffsetFile(dir, "", "")
	if path != filepath.Join(dir, "poll-offset") {
		t.Errorf("keyring mode (no token) should keep the shared name, got %q", path)
	}
	if legacy != "" {
		t.Errorf("no migration when the default is already the shared name, got %q", legacy)
	}
}

func TestMigrateOffset_SeedsFromLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "poll-offset")
	saveOffset(legacy, 4242)
	cfg := Config{OffsetFile: filepath.Join(dir, "poll-offset-123"), LegacyOffsetFile: legacy}

	if got := migrateOffset(cfg); got != 4242 {
		t.Fatalf("migrateOffset = %d, want 4242 (seeded from legacy)", got)
	}
	// The value must be written to the per-bot file so it survives even if legacy is removed.
	if got := loadOffset(cfg.OffsetFile); got != 4242 {
		t.Fatalf("per-bot cursor = %d, want 4242 written on migration", got)
	}
}

func TestMigrateOffset_PerBotWins(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "poll-offset")
	perBot := filepath.Join(dir, "poll-offset-123")
	saveOffset(legacy, 111)
	saveOffset(perBot, 999)
	cfg := Config{OffsetFile: perBot, LegacyOffsetFile: legacy}
	if got := migrateOffset(cfg); got != 999 {
		t.Fatalf("existing per-bot cursor must win over legacy, got %d", got)
	}
}

func TestMigrateOffset_NoLegacy(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{OffsetFile: filepath.Join(dir, "poll-offset-123"), LegacyOffsetFile: ""}
	if got := migrateOffset(cfg); got != 0 {
		t.Fatalf("no legacy configured → 0, got %d", got)
	}
}

func TestLockOffsetFile_SecondAcquireFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poll-offset-1")

	unlock, err := lockOffsetFile(path)
	if err != nil {
		t.Fatalf("first lock should succeed: %v", err)
	}

	if _, err := lockOffsetFile(path); err == nil {
		t.Fatal("second lock on a held offset file must fail")
	}

	unlock() // release, then re-acquiring must succeed
	unlock2, err := lockOffsetFile(path)
	if err != nil {
		t.Fatalf("re-lock after release should succeed: %v", err)
	}
	unlock2()
}

func TestLockOffsetFile_EmptyPathNoop(t *testing.T) {
	unlock, err := lockOffsetFile("")
	if err != nil {
		t.Fatalf("empty path should be a no-op, got %v", err)
	}
	unlock()
}

func TestLockOffsetFile_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "poll-offset")
	unlock, err := lockOffsetFile(path)
	if err != nil {
		t.Fatalf("lock should create parent dirs: %v", err)
	}
	defer unlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("offset file should exist after lock: %v", err)
	}
}
