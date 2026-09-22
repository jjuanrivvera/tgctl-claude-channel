package main

import (
	"io"
	"regexp"
	"strings"
	"sync/atomic"
)

// The Telegram Bot API carries its credential in the URL path, so anything that prints a
// request URL prints the secret. Go's *url.Error does exactly that for any transport failure,
// which is how a read timeout put a live bot token into a tool result — and from there into
// the agent's transcript on disk (jjuanrivvera/tgctl#21).
//
// tgctl redacts at the source. This file is the channel's own layer: every JSON-RPC frame we
// emit and every line we log passes through it, so a leak upstream (an old tgctl binary on
// the host, a future code path that forgets) still cannot reach the caller.

// tokenInURLRe matches a token where the Bot API puts it: after "/bot" for method calls and
// after "/file/bot" for downloads. The numeric bot id is kept — it is the bot's user id, not
// a secret — so a redacted message still says which bot failed.
var tokenInURLRe = regexp.MustCompile(`(/(?:file/)?bot)(-?\d+):[A-Za-z0-9_-]+`)

// bareTokenRe matches a token outside a URL (an env var echoed into an error, a config dump).
// It demands both halves of the "<bot_id>:<hash>" shape, with a hash long enough that ordinary
// "id:value" text does not match.
var bareTokenRe = regexp.MustCompile(`\b(\d{5,}):[A-Za-z0-9_-]{20,}\b`)

// liveToken is the credential this process was handed, when it was handed one at all (with
// TGCTL_TOKEN unset, tgctl reads it from the keyring and we never see it). Knowing the exact
// value catches shapes the patterns would miss, such as a self-hosted Bot API server's token.
var liveToken atomic.Value // string

func setLiveToken(tok string) { liveToken.Store(tok) }

func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	if tok, _ := liveToken.Load().(string); tok != "" {
		s = strings.ReplaceAll(s, tok, redactToken(tok))
	}
	s = tokenInURLRe.ReplaceAllString(s, "${1}${2}:<redacted>")
	return bareTokenRe.ReplaceAllString(s, "${1}:<redacted>")
}

// redactToken masks a token for display, keeping the non-secret bot id prefix.
func redactToken(tok string) string {
	id, _, ok := strings.Cut(tok, ":")
	if !ok || id == "" {
		return "<redacted>"
	}
	return id + ":<redacted>"
}

// redactingWriter scrubs secrets out of everything written through it. main wraps the
// process log in one, so a token in an error we merely log never lands on the host either.
type redactingWriter struct{ w io.Writer }

func (rw redactingWriter) Write(p []byte) (int, error) {
	red := redactSecrets(string(p))
	if red == string(p) {
		return rw.w.Write(p)
	}
	if _, err := rw.w.Write([]byte(red)); err != nil {
		return 0, err
	}
	// Report the caller's own length: a short count would look like a failed write to
	// log.Output, and the redacted form is deliberately a different size.
	return len(p), nil
}
