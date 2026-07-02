package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Telegram Update subset we consume from `tgctl updates get -o json`.
type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *tgMessage     `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

// callbackQuery is an inline-keyboard button tap. Message carries the chat + the
// message the buttons are on; Data is the button's callback_data.
type callbackQuery struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      tgChat `json:"chat"`
	From      tgUser `json:"from"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// runInbound drives Telegram → session by LONG-POLLING `tgctl updates get`, not a
// webhook. Polling keeps the channel independent of any public ingress: no tunnel,
// no Cloudflare, no inbound port — the VPS reaches out to Telegram. getUpdates and
// a webhook are mutually exclusive, so we clear any webhook first.
func (s *server) runInbound(ctx context.Context, cfg Config) error {
	deleteWebhook(ctx, cfg)

	offset := loadOffset(cfg.OffsetFile)
	if offset == 0 {
		// Fresh start with no saved cursor: skip any backlog so we don't replay
		// stale messages the user sent before the channel came up.
		offset = latestOffset(ctx, cfg)
		saveOffset(cfg.OffsetFile, offset)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := getUpdates(ctx, cfg, offset)
		if err != nil {
			// Transient (network, or a 409 if a webhook is momentarily still set):
			// log, back off, retry. Never crash the channel on a poll error.
			log.Printf("updates get: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, up := range ups {
			if up.UpdateID >= offset {
				offset = up.UpdateID + 1 // confirm/advance past this update
			}
			switch {
			case up.Message != nil:
				if n := buildNotification(up, cfg.Allow); n != nil {
					s.out.send(n)
					s.onInbound(cfg, up.Message) // ack reaction + "typing…"
				}
			case up.CallbackQuery != nil:
				if n := buildCallback(up.CallbackQuery, cfg.Allow); n != nil {
					s.out.send(n)
					if up.CallbackQuery.Message != nil {
						s.typing.start(strconv.FormatInt(up.CallbackQuery.Message.Chat.ID, 10))
					}
				}
			}
		}
		if len(ups) > 0 {
			saveOffset(cfg.OffsetFile, offset)
		}
	}
}

// getUpdates runs one long-poll. tgctl blocks up to --timeout seconds server-side
// and returns a JSON array of updates (possibly empty / null).
func getUpdates(ctx context.Context, cfg Config, offset int64) ([]update, error) {
	args := []string{"updates", "get", "-o", "json", "--timeout", "30", "--limit", "100", "--allowed-updates", "message,callback_query"}
	if offset > 0 {
		args = append(args, "--offset", strconv.FormatInt(offset, 10))
	}
	out, err := tgctlOutput(ctx, cfg, args...)
	if err != nil {
		return nil, err
	}
	return parseUpdates(out)
}

// latestOffset returns (last update_id + 1) so a fresh start ignores the backlog.
// Uses Telegram's negative-offset trick (offset=-1 → only the most recent update).
func latestOffset(ctx context.Context, cfg Config) int64 {
	out, err := tgctlOutput(ctx, cfg, "updates", "get", "-o", "json", "--offset", "-1", "--limit", "1")
	if err != nil {
		return 0
	}
	ups, err := parseUpdates(out)
	if err != nil || len(ups) == 0 {
		return 0
	}
	return ups[len(ups)-1].UpdateID + 1
}

// deleteWebhook clears any registered webhook so getUpdates won't 409. Best-effort.
func deleteWebhook(ctx context.Context, cfg Config) {
	_, _ = tgctlOutput(ctx, cfg, "webhook", "delete")
}

// onInbound gives the sender immediate feedback the moment a message is accepted:
// an optional "seen" reaction, and a live "typing…" indicator that runs until
// Claude calls reply. Both are best-effort and never block the inject path.
func (s *server) onInbound(cfg Config, m *tgMessage) {
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	if cfg.AckReaction != "" {
		go func() { _, _ = s.tg.react(chatID, strconv.FormatInt(m.MessageID, 10), cfg.AckReaction) }()
	}
	s.typing.start(chatID)
}

func tgctlOutput(ctx context.Context, cfg Config, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cfg.TgctlBin, args...)
	cmd.Env = append(os.Environ(), "TGCTL_TOKEN="+cfg.BotToken)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func parseUpdates(out []byte) ([]update, error) {
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 || string(out) == "null" {
		return nil, nil
	}
	var ups []update
	if err := json.Unmarshal(out, &ups); err != nil {
		return nil, err
	}
	return ups, nil
}

func loadOffset(path string) int64 {
	if path == "" {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func saveOffset(path string, offset int64) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(offset, 10)), 0o600)
}

// pump decodes a STREAM of Updates (one JSON value at a time), gates each on the
// sender, and emits a channel notification per accepted message. Retained for the
// unit tests and any streaming transport; the live path uses getUpdates polling.
func pump(r io.Reader, allow map[int64]bool, emit func(any)) {
	dec := json.NewDecoder(r)
	for {
		var up update
		if err := dec.Decode(&up); err != nil {
			return // EOF or stream closed
		}
		if n := buildNotification(up, allow); n != nil {
			emit(n)
		}
	}
}

// buildNotification gates on SENDER identity (user_id), not chat — the allowlist
// is the core security boundary: only configured users can drive the agent.
// Returns nil to drop the update.
func buildNotification(up update, allow map[int64]bool) any {
	m := up.Message
	if m == nil || m.Text == "" {
		return nil
	}
	if !allow[m.From.ID] {
		return nil // sender not allowlisted → drop silently
	}
	return notification("notifications/claude/channel", map[string]any{
		"content": m.Text,
		"meta": map[string]string{
			"source":     "telegram",
			"chat_id":    strconv.FormatInt(m.Chat.ID, 10),
			"user_id":    strconv.FormatInt(m.From.ID, 10),
			"message_id": strconv.FormatInt(m.MessageID, 10),
			"username":   m.From.Username,
		},
	})
}

// buildCallback turns an inline-keyboard button tap into a channel turn. The
// callback_query_id in meta must be passed to the answer_callback tool (Telegram
// shows a spinner on the button until it's answered). Gated on the tapper's user_id.
func buildCallback(cq *callbackQuery, allow map[int64]bool) any {
	if cq == nil || !allow[cq.From.ID] {
		return nil
	}
	var chatID, msgID int64
	if cq.Message != nil {
		chatID = cq.Message.Chat.ID
		msgID = cq.Message.MessageID
	}
	return notification("notifications/claude/channel", map[string]any{
		"content": "[button tap] " + cq.Data,
		"meta": map[string]string{
			"source":            "telegram",
			"kind":              "callback_query",
			"callback_query_id": cq.ID,
			"data":              cq.Data,
			"chat_id":           strconv.FormatInt(chatID, 10),
			"user_id":           strconv.FormatInt(cq.From.ID, 10),
			"message_id":        strconv.FormatInt(msgID, 10),
			"username":          cq.From.Username,
		},
	})
}
