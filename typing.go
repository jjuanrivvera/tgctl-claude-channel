package main

import (
	"context"
	"sync"
	"time"
)

// typingManager keeps a Telegram "typing…" chat action alive for a chat while
// Claude is working on a reply. Telegram chat actions auto-expire after ~5s, so we
// re-send every 4s until the reply lands (or a safety timeout). Concurrency-safe:
// the inbound poller start()s it when a message is injected; the MCP reply handler
// stop()s it when Claude sends the answer. A nil *typingManager is a no-op, so the
// server and its tests work whether or not typing is wired.
type typingManager struct {
	tg  transport
	mu  sync.Mutex
	act map[string]context.CancelFunc
}

func newTypingManager(tg transport) *typingManager {
	return &typingManager{tg: tg, act: map[string]context.CancelFunc{}}
}

func (t *typingManager) start(chatID string) {
	if t == nil || chatID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.act[chatID]; ok {
		return // already typing for this chat
	}
	// Safety cap: never leave "typing…" pinned forever if a reply never comes.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.act[chatID] = cancel
	go t.loop(ctx, chatID)
}

func (t *typingManager) loop(ctx context.Context, chatID string) {
	for {
		_, _ = t.tg.action(chatID, "typing") // best-effort; a dropped action isn't fatal
		select {
		case <-ctx.Done():
			return
		case <-time.After(4 * time.Second):
		}
	}
}

func (t *typingManager) stop(chatID string) {
	if t == nil || chatID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel, ok := t.act[chatID]; ok {
		cancel()
		delete(t.act, chatID)
	}
}
