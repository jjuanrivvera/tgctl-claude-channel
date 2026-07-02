package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Permission relay. Declaring the `claude/channel/permission` capability lets Claude
// Code run WITH its permission sandbox on: when it wants to use a tool, it sends a
// permission_request, we relay it to the operator's Telegram as inline Allow/Deny
// buttons (or a "yes <code>" text reply), and send the decision back. This is what
// removes the need for --dangerously-skip-permissions.

// Reply/callback codes are 5 letters a-z minus 'l' (the CC channel-permission spec —
// avoids 1/l/I ambiguity). Case-insensitive for phone autocorrect.
var (
	permReplyRe    = regexp.MustCompile(`(?i)^\s*(y|yes|n|no)\s+([a-km-z]{5})\s*$`)
	permCallbackRe = regexp.MustCompile(`^perm:(allow|deny|more):([a-km-z]{5})$`)
)

type permDetail struct {
	toolName, description, inputPreview string
}

type permissionManager struct {
	mu      sync.Mutex
	pending map[string]permDetail
}

func newPermissionManager() *permissionManager {
	return &permissionManager{pending: map[string]permDetail{}}
}

// onRequest relays a Claude permission request to every allowlisted DM.
func (pm *permissionManager) onRequest(s *server, params json.RawMessage) {
	var p struct {
		RequestID    string `json:"request_id"`
		ToolName     string `json:"tool_name"`
		Description  string `json:"description"`
		InputPreview string `json:"input_preview"`
	}
	if json.Unmarshal(params, &p) != nil || p.RequestID == "" {
		return
	}
	pm.mu.Lock()
	pm.pending[p.RequestID] = permDetail{p.ToolName, p.Description, p.InputPreview}
	pm.mu.Unlock()

	kb := kbd([][]map[string]any{{
		{"text": "See more", "callback_data": "perm:more:" + p.RequestID},
		{"text": "✅ Allow", "callback_data": "perm:allow:" + p.RequestID},
		{"text": "❌ Deny", "callback_data": "perm:deny:" + p.RequestID},
	}})
	text := "🔐 Permission: " + p.ToolName
	for _, chatID := range s.store.read().AllowFrom {
		go func(chat string) {
			data, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text, "reply_markup": kb})
			_, _ = s.tg.cmd("api", "sendMessage", "--data", string(data))
		}(chatID)
	}
}

// handleTextReply resolves a permission via a "yes xxxxx" / "no xxxxx" message. Returns
// whether it consumed the message. The sender is already gate-approved by the caller.
func (pm *permissionManager) handleTextReply(s *server, m *message, chatID, text string) bool {
	mm := permReplyRe.FindStringSubmatch(text)
	if mm == nil {
		return false
	}
	behavior, emoji := "deny", "❌"
	if strings.HasPrefix(strings.ToLower(mm[1]), "y") {
		behavior, emoji = "allow", "✅"
	}
	pm.emit(s, strings.ToLower(mm[2]), behavior)
	if m.MessageID != 0 {
		go func() { _, _ = s.tg.react(chatID, strconv.FormatInt(m.MessageID, 10), emoji) }()
	}
	return true
}

// handleCallback resolves/expands a permission via its inline buttons. Returns whether
// the callback was a permission button (and thus should not surface as a turn).
func (pm *permissionManager) handleCallback(s *server, cq *callbackQuery) bool {
	mm := permCallbackRe.FindStringSubmatch(cq.Data)
	if mm == nil {
		return false
	}
	if !containsStr(s.store.read().AllowFrom, strconv.FormatInt(cq.From.ID, 10)) {
		_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", "Not authorized.")
		return true
	}
	behavior, requestID := mm[1], mm[2]

	if behavior == "more" {
		pm.mu.Lock()
		d, ok := pm.pending[requestID]
		pm.mu.Unlock()
		if !ok {
			_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", "Details no longer available.")
			return true
		}
		pretty := d.inputPreview
		var v any
		if json.Unmarshal([]byte(d.inputPreview), &v) == nil {
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				pretty = string(b)
			}
		}
		expanded := "🔐 Permission: " + d.toolName + "\n\ntool_name: " + d.toolName + "\ndescription: " + d.description + "\ninput_preview:\n" + pretty
		if cq.Message != nil {
			kb := kbd([][]map[string]any{{
				{"text": "✅ Allow", "callback_data": "perm:allow:" + requestID},
				{"text": "❌ Deny", "callback_data": "perm:deny:" + requestID},
			}})
			data, _ := json.Marshal(map[string]any{"chat_id": strconv.FormatInt(cq.Message.Chat.ID, 10), "message_id": cq.Message.MessageID, "text": expanded, "reply_markup": kb})
			_, _ = s.tg.cmd("api", "editMessageText", "--data", string(data))
		}
		_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID)
		return true
	}

	pm.emit(s, requestID, behavior)
	label := "✅ Allowed"
	if behavior == "deny" {
		label = "❌ Denied"
	}
	_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", label)
	// Replace the buttons with the outcome so the same request can't be answered twice.
	if cq.Message != nil {
		data, _ := json.Marshal(map[string]any{"chat_id": strconv.FormatInt(cq.Message.Chat.ID, 10), "message_id": cq.Message.MessageID, "text": "🔐 Permission\n\n" + label})
		_, _ = s.tg.cmd("api", "editMessageText", "--data", string(data))
	}
	return true
}

func (pm *permissionManager) emit(s *server, requestID, behavior string) {
	pm.mu.Lock()
	delete(pm.pending, requestID)
	pm.mu.Unlock()
	s.out.send(notification("notifications/claude/channel/permission", map[string]any{
		"request_id": requestID,
		"behavior":   behavior,
	}))
}

func kbd(rows [][]map[string]any) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}
