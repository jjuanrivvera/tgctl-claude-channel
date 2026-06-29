package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// Telegram Update subset we consume from `tgctl webhook listen -o json`.
type update struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
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

// runInbound starts tgctl's webhook receiver and forwards each allowlisted
// message into the Claude session as a channel notification. tgctl is the only
// thing that touches the Bot API; this just consumes its JSON stream.
func (s *server) runInbound(ctx context.Context, cfg Config) error {
	args := []string{"webhook", "listen", "-o", "json", "--port", cfg.Port}
	if cfg.Secret != "" {
		args = append(args, "--secret-token", cfg.Secret)
	}
	if cfg.SetURL != "" {
		args = append(args, "--set-url", cfg.SetURL)
	}
	cmd := exec.CommandContext(ctx, cfg.TgctlBin, args...)
	cmd.Env = append(os.Environ(), "TGCTL_TOKEN="+cfg.BotToken)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pump(stdout, cfg.Allow, s.out.send)
	return cmd.Wait()
}

// pump decodes tgctl's JSON Update stream, gates each on the sender, and emits a
// channel notification per accepted message. Factored out so it is unit-testable
// without spawning tgctl.
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
