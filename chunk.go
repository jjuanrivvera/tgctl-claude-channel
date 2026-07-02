package main

import "strings"

// telegramMaxChars is Telegram's hard per-message limit. Chunking counts runes,
// which slightly under-counts vs Telegram's UTF-16 units — erring toward smaller
// chunks, never larger, so a reply is never rejected for length.
const telegramMaxChars = 4096

// chunk splits text so no piece exceeds limit runes. mode "newline" prefers to cut
// on paragraph/line/space boundaries; any other mode is a hard rune cut.
func chunk(text string, limit int, mode string) []string {
	if limit <= 0 || limit > telegramMaxChars {
		limit = telegramMaxChars
	}
	r := []rune(text)
	if len(r) <= limit {
		return []string{text}
	}
	var out []string
	for len(r) > limit {
		cut := limit
		if mode == "newline" {
			cut = boundary(r, limit)
		}
		out = append(out, string(r[:cut]))
		// drop leading newlines on the remainder so paragraphs read cleanly
		r = []rune(strings.TrimLeft(string(r[cut:]), "\n"))
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

// boundary finds the best split point at or before limit: last paragraph break,
// then last newline, then last space — but only if past the halfway mark, so we
// don't produce a tiny sliver. Falls back to a hard cut at limit.
func boundary(r []rune, limit int) int {
	half := limit / 2
	if i := lastIndexRune(r, limit, "\n\n"); i > half {
		return i
	}
	if i := lastIndexRune(r, limit, "\n"); i > half {
		return i
	}
	if i := lastIndexRune(r, limit, " "); i > 0 {
		return i
	}
	return limit
}

// lastIndexRune returns the rune index of the last occurrence of sep within r[:within],
// or -1. Works on runes (not bytes) so multibyte text splits correctly.
func lastIndexRune(r []rune, within int, sep string) int {
	if within > len(r) {
		within = len(r)
	}
	s := []rune(sep)
	for i := within - len(s); i >= 0; i-- {
		if string(r[i:i+len(s)]) == sep {
			return i
		}
	}
	return -1
}
