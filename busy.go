package main

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// defaultBusyNoticeText is the one-time heads-up sent when a turn goes unanswered past
// the configured delay. Spanish, because the operator (Juan) reads the bot in Spanish;
// override with TGCTL_CHANNEL_BUSY_NOTICE_TEXT.
const defaultBusyNoticeText = "⏳ La sesión está ocupada o esperando una respuesta. Tu mensaje quedó en cola y se procesará en cuanto se libere."

// busyNotifier sends a single "busy / waiting for an answer" notice to a chat when the
// session has not replied within a configured delay after a turn was delivered.
//
// Why this exists (see issue #5): the channel delivers an inbound turn as a
// fire-and-forget notifications/claude/channel notification and gets no acknowledgement
// back — it only learns the session is alive when a reply tool call returns. When the
// session is parked on an interactive prompt (an AskUserQuestion / modal menu) it stops
// processing turns, so no reply ever arrives and, from Telegram, the bot looks dead. The
// channel process cannot see the session's TUI state, but it CAN observe "a turn was
// delivered and no reply came within N seconds" — enough to send one debounced heads-up
// so the sender knows the message was queued, not lost.
//
// The lifecycle mirrors typingManager: start() when a turn is delivered, stop() when the
// reply lands. It is deliberately conservative — one notice per in-flight turn, cancelled
// the moment a reply arrives. A nil *busyNotifier is a no-op, and a non-positive delay
// disables the feature entirely (off by default), so the server and its tests work
// whether or not it is wired or enabled.
type busyNotifier struct {
	tg      transport
	delay   time.Duration
	text    string
	mu      sync.Mutex
	pending map[string]context.CancelFunc
}

// newBusyNotifier returns a notifier, or nil when delay <= 0 (feature disabled). A nil
// return is safe: every method is a no-op on a nil receiver.
func newBusyNotifier(tg transport, delay time.Duration, text string) *busyNotifier {
	if delay <= 0 {
		return nil
	}
	if text == "" {
		text = defaultBusyNoticeText
	}
	return &busyNotifier{tg: tg, delay: delay, text: text, pending: map[string]context.CancelFunc{}}
}

// start arms a one-shot timer for chatID. If a reply doesn't stop() it before the delay
// elapses, one notice is sent. A second start() for a chat already in flight is ignored,
// so a burst of queued messages yields at most one notice per unanswered turn.
func (b *busyNotifier) start(chatID string) {
	if b == nil || chatID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[chatID]; ok {
		return // a notice is already scheduled for this chat's in-flight turn
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.pending[chatID] = cancel
	go b.wait(ctx, chatID)
}

func (b *busyNotifier) wait(ctx context.Context, chatID string) {
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return // reply landed before the delay — stay quiet
	case <-timer.C:
	}
	_, _ = b.tg.send(chatID, b.text) // best-effort; a dropped notice isn't fatal
	b.mu.Lock()
	delete(b.pending, chatID)
	b.mu.Unlock()
}

// stop cancels a pending notice for chatID (the reply arrived in time). Idempotent.
func (b *busyNotifier) stop(chatID string) {
	if b == nil || chatID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cancel, ok := b.pending[chatID]; ok {
		cancel()
		delete(b.pending, chatID)
	}
}

// parseBusyDelay reads TGCTL_CHANNEL_BUSY_NOTICE_DELAY. It accepts a Go duration
// ("45s", "2m") or a bare number of seconds ("45"). Empty, zero, negative, or
// unparseable → 0, which disables the feature (off by default).
func parseBusyDelay(v string) time.Duration {
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return 0
		}
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}
