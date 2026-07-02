package main

import (
	"strings"
	"testing"
)

func TestChunk_ShortReturnsWhole(t *testing.T) {
	got := chunk("hola", 100, "length")
	if len(got) != 1 || got[0] != "hola" {
		t.Fatalf("short text should be one chunk, got %v", got)
	}
}

func TestChunk_HardLength(t *testing.T) {
	s := strings.Repeat("a", 250)
	got := chunk(s, 100, "length")
	if len(got) != 3 {
		t.Fatalf("250 chars @100 → 3 chunks, got %d", len(got))
	}
	if strings.Join(got, "") != s {
		t.Fatal("chunks must rejoin to the original (length mode)")
	}
	for _, c := range got {
		if len([]rune(c)) > 100 {
			t.Fatalf("chunk exceeds limit: %d", len([]rune(c)))
		}
	}
}

func TestChunk_NewlinePrefersParagraph(t *testing.T) {
	text := strings.Repeat("a", 60) + "\n\n" + strings.Repeat("b", 60)
	got := chunk(text, 100, "newline")
	if len(got) != 2 {
		t.Fatalf("expected split into 2 at the paragraph break, got %d: %v", len(got), got)
	}
	if got[0] != strings.Repeat("a", 60) {
		t.Fatalf("first chunk should end at the blank line, got %q", got[0])
	}
	if got[1] != strings.Repeat("b", 60) {
		t.Fatalf("leading newlines should be trimmed, got %q", got[1])
	}
}

func TestChunk_MultibyteCountsRunes(t *testing.T) {
	// 200 emoji (4 bytes each in UTF-8) — must chunk by rune count, not byte count.
	s := strings.Repeat("😀", 200)
	got := chunk(s, 100, "length")
	if len(got) != 2 {
		t.Fatalf("200 runes @100 → 2 chunks, got %d", len(got))
	}
	if strings.Join(got, "") != s {
		t.Fatal("multibyte chunks must rejoin cleanly")
	}
}

func TestChunk_ZeroLimitDefaults(t *testing.T) {
	s := strings.Repeat("a", 5000)
	got := chunk(s, 0, "length")
	if len(got) != 2 { // 5000 @ 4096 default
		t.Fatalf("zero limit should default to 4096 → 2 chunks, got %d", len(got))
	}
}

func TestLastIndexRune(t *testing.T) {
	r := []rune("a b c")
	if got := lastIndexRune(r, 5, " "); got != 3 {
		t.Fatalf("last space index = %d, want 3", got)
	}
	if got := lastIndexRune(r, 5, "z"); got != -1 {
		t.Fatalf("missing sep should be -1, got %d", got)
	}
}
