package main

import "strings"

// handleCommand answers /start, /help and /status. Commands are DM-only (answering in
// a group would leak pairing codes to other members) and are never relayed to Claude.
func (s *server) handleCommand(m *message, chatType, senderID, chatID string) {
	if chatType != "private" {
		return
	}
	acc := s.store.read()
	if acc.DMPolicy == policyDisabled {
		return
	}
	if acc.DMPolicy == policyAllowlist && !containsStr(acc.AllowFrom, senderID) {
		return
	}
	switch commandName(m.Text) {
	case "start":
		_, _ = s.tg.send(chatID,
			"This bot bridges Telegram to a Claude Code session.\n\n"+
				"To pair:\n"+
				"1. DM me anything — you'll get a 6-char code\n"+
				"2. In Claude Code: /telegram:access pair <code>\n\n"+
				"After that, messages here reach that session.")
	case "help":
		_, _ = s.tg.send(chatID,
			"Messages you send here route to a paired Claude Code session. Text, photos and "+
				"files are forwarded; replies, reactions, polls and buttons come back.\n\n"+
				"/start — pairing instructions\n"+
				"/status — check your pairing state")
	case "status":
		s.statusCommand(acc, senderID, chatID)
	}
}

func (s *server) statusCommand(acc access, senderID, chatID string) {
	if containsStr(acc.AllowFrom, senderID) {
		_, _ = s.tg.send(chatID, "Paired as "+senderID+".")
		return
	}
	for code, p := range acc.Pending {
		if p.SenderID == senderID {
			_, _ = s.tg.send(chatID, "Pending pairing — run in Claude Code:\n\n/telegram:access pair "+code)
			return
		}
	}
	_, _ = s.tg.send(chatID, "Not paired. Send me a message to get a pairing code.")
}

// commandName extracts "start" from "/start@thebot arg".
func commandName(text string) string {
	t := strings.TrimPrefix(text, "/")
	if i := strings.IndexAny(t, " @"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}
