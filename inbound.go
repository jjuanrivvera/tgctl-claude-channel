package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// --- Telegram Update subset we parse from `tgctl updates get -o json` ------------------

type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	Text            string      `json:"text"`
	Caption         string      `json:"caption"`
	Date            int64       `json:"date"`
	Chat            tgChat      `json:"chat"`
	From            tgUser      `json:"from"`
	Entities        []msgEntity `json:"entities"`
	CaptionEntities []msgEntity `json:"caption_entities"`
	ReplyTo         *message    `json:"reply_to_message"`
	Photo           []fileRef   `json:"photo"`
	Document        *fileRef    `json:"document"`
	Voice           *fileRef    `json:"voice"`
	Audio           *fileRef    `json:"audio"`
	Video           *fileRef    `json:"video"`
	VideoNote       *fileRef    `json:"video_note"`
	Sticker         *sticker    `json:"sticker"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
}

type msgEntity struct {
	Type   string  `json:"type"`
	Offset int     `json:"offset"`
	Length int     `json:"length"`
	User   *tgUser `json:"user"`
}

type fileRef struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
	MimeType     string `json:"mime_type"`
	Title        string `json:"title"`
}

type sticker struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Emoji        string `json:"emoji"`
	FileSize     int64  `json:"file_size"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    tgUser   `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

// --- poller ---------------------------------------------------------------------------

// runInbound drives Telegram → session by LONG-POLLING `tgctl updates get`. Polling
// needs no public endpoint (no webhook, no tunnel, immune to edge WAFs). getUpdates
// and a webhook are mutually exclusive, so we clear any webhook first.
func (s *server) runInbound(ctx context.Context) error {
	deleteWebhook(ctx, s.cfg)

	offset := loadOffset(s.cfg.OffsetFile)
	if offset == 0 {
		offset = latestOffset(ctx, s.cfg)
		saveOffset(s.cfg.OffsetFile, offset)
	}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := getUpdates(ctx, s.cfg, offset)
		if err != nil {
			// Exponential backoff on any error (network, or 409 if a webhook lingers).
			log.Printf("updates get: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, up := range ups {
			if up.UpdateID >= offset {
				offset = up.UpdateID + 1
			}
			s.handleUpdate(up)
		}
		if len(ups) > 0 {
			saveOffset(s.cfg.OffsetFile, offset)
		}
	}
}

func (s *server) handleUpdate(up update) {
	switch {
	case up.Message != nil:
		s.handleMessage(up.Message)
	case up.CallbackQuery != nil:
		s.handleCallback(up.CallbackQuery)
	}
}

// handleMessage runs the gate, then commands / permission-reply / media handling, and
// finally delivers the message into the Claude session as a channel notification.
func (s *server) handleMessage(m *message) {
	chatType := m.Chat.Type
	senderID := strconv.FormatInt(m.From.ID, 10)
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	text := m.textOrCaption()

	// Bot commands are handled here and never relayed as a turn. A configured command
	// handler claims its own commands (operator-only — these can drive the host session);
	// everything else falls through to the built-in pairing commands.
	if isCommand(text) {
		if s.cmdHook.handles(commandName(text)) {
			if containsStr(s.store.read().AllowFrom, senderID) {
				s.cmdHook.run(s, m, chatID, commandName(text), commandArgs(text))
			}
			return
		}
		s.handleCommand(m, chatType, senderID, chatID)
		return
	}

	var mentioned bool
	if chatType == "group" || chatType == "supergroup" {
		acc := s.store.read()
		mentioned = isMentioned(text, s.botUser, append(m.Entities, m.CaptionEntities...), m.replyToBotUsername(), acc.MentionPatterns)
	}

	var res gateResult
	s.store.update(func(a *access) bool {
		r, changed := gate(a, chatType, senderID, chatID, mentioned, nowMillis())
		res = r
		return changed
	})

	switch res.action {
	case gateDrop:
		return
	case gatePair:
		lead := "Pairing required"
		if res.isResend {
			lead = "Still pending"
		}
		_, _ = s.tg.send(chatID, lead+" — run in Claude Code:\n\n/telegram:access pair "+res.code)
		return
	}

	// Permission-reply intercept: "yes xxxxx" / "no xxxxx" resolves a pending
	// approval instead of relaying as chat. Sender is already gate-approved.
	if s.perms.handleTextReply(s, m, chatID, text) {
		return
	}

	acc := s.store.read()
	// Immediate feedback: typing indicator + optional "seen" reaction.
	s.typing.start(chatID)
	if ack := acc.ack(); ack != "" && m.MessageID != 0 {
		go func() { _, _ = s.tg.react(chatID, strconv.FormatInt(m.MessageID, 10), ack) }()
	}

	imagePath, attach := s.collectMedia(m)
	s.out.send(buildMessageNotification(m, chatID, senderID, m.defaultCaption(), imagePath, attach))
}

func (s *server) handleCallback(cq *callbackQuery) {
	// Internal permission buttons (perm:allow/deny/more:<id>) are handled here and
	// never surface as a turn.
	if s.perms.handleCallback(s, cq) {
		return
	}
	senderID := strconv.FormatInt(cq.From.ID, 10)
	// Handler-owned button taps drive the host session (operator-only), not the model.
	if s.cmdHook.ownsCallback(cq.Data) {
		if containsStr(s.store.read().AllowFrom, senderID) {
			s.cmdHook.callback(s, cq)
		} else {
			_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", "Not authorized.")
		}
		return
	}
	acc := s.store.read()
	if !containsStr(acc.AllowFrom, senderID) {
		if cq.Message == nil {
			return
		}
		if _, ok := acc.Groups[strconv.FormatInt(cq.Message.Chat.ID, 10)]; !ok {
			_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", "Not authorized.")
			return
		}
	}
	if cq.Message != nil {
		s.typing.start(strconv.FormatInt(cq.Message.Chat.ID, 10))
	}
	s.out.send(buildCallbackNotification(cq))
}

// --- notification builders ------------------------------------------------------------

func buildMessageNotification(m *message, chatID, senderID, content, imagePath string, attach *attachment) any {
	meta := map[string]string{
		"source":     "telegram",
		"chat_id":    chatID,
		"user_id":    senderID,
		"username":   m.From.Username,
		"user":       m.From.displayName(),
		"message_id": strconv.FormatInt(m.MessageID, 10),
		"ts":         time.Unix(m.Date, 0).UTC().Format(time.RFC3339),
	}
	if imagePath != "" {
		meta["image_path"] = imagePath
	}
	if attach != nil {
		meta["attachment_kind"] = attach.Kind
		meta["attachment_file_id"] = attach.FileID
		if attach.Size > 0 {
			meta["attachment_size"] = strconv.FormatInt(attach.Size, 10)
		}
		if attach.Mime != "" {
			meta["attachment_mime"] = attach.Mime
		}
		if attach.Name != "" {
			meta["attachment_name"] = attach.Name
		}
	}
	return notification("notifications/claude/channel", map[string]any{"content": content, "meta": meta})
}

func buildCallbackNotification(cq *callbackQuery) any {
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
			"username":          cq.From.Username,
			"message_id":        strconv.FormatInt(msgID, 10),
		},
	})
}

// --- message helpers ------------------------------------------------------------------

func (m *message) textOrCaption() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

func (m *message) replyToBotUsername() string {
	if m.ReplyTo != nil {
		return m.ReplyTo.From.Username
	}
	return ""
}

func (u tgUser) displayName() string {
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	return strconv.FormatInt(u.ID, 10)
}

func (a access) ack() string {
	if a.AckReaction != nil {
		return *a.AckReaction
	}
	return "👀"
}

func isCommand(text string) bool { return strings.HasPrefix(text, "/") }

// --- tgctl plumbing -------------------------------------------------------------------

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

func deleteWebhook(ctx context.Context, cfg Config) {
	_, _ = tgctlOutput(ctx, cfg, "webhook", "delete")
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
