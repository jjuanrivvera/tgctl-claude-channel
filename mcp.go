package main

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
)

// The MCP stdio transport is newline-delimited JSON-RPC 2.0. We hand-roll it
// instead of using a Go MCP SDK because the Claude Code "channel" contract needs
// a CUSTOM server notification (notifications/claude/channel) and a custom
// experimental capability — neither of which the SDKs expose cleanly. Owning
// stdout ourselves makes both trivial while still speaking strict JSON-RPC 2.0.

const channelCapability = "claude/channel"

type inMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent/null on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (m inMsg) isNotification() bool {
	return len(m.ID) == 0 || string(m.ID) == "null"
}

// out serializes writes to stdout. The inbound pump and the request handler both
// emit frames, so a single mutex-guarded encoder keeps them from interleaving.
type out struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newOut(w io.Writer) *out { return &out{enc: json.NewEncoder(w)} }

func (o *out) send(v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.enc.Encode(v) // Encode appends '\n' → newline-delimited framing
}

func result(id json.RawMessage, res any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": res}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func notification(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

type server struct {
	out    *out
	tg     transport
	typing *typingManager // may be nil (tests); typingManager methods are nil-safe
}

// serve runs the JSON-RPC request loop until the client (Claude Code) closes stdin.
func (s *server) serve(r io.Reader) {
	dec := json.NewDecoder(r)
	for {
		var m inMsg
		if err := dec.Decode(&m); err != nil {
			return // EOF / client disconnected
		}
		s.dispatch(m)
	}
}

func (s *server) dispatch(m inMsg) {
	switch m.Method {
	case "initialize":
		s.out.send(result(m.ID, initializeResult(m.Params)))
	case "notifications/initialized":
		// handshake complete; nothing to do
	case "ping":
		s.out.send(result(m.ID, map[string]any{}))
	case "tools/list":
		s.out.send(result(m.ID, map[string]any{"tools": toolDefs()}))
	case "tools/call":
		s.handleToolCall(m)
	default:
		if !m.isNotification() {
			s.out.send(rpcErr(m.ID, -32601, "method not found: "+m.Method))
		}
	}
}

func initializeResult(params json.RawMessage) map[string]any {
	pv := "2025-06-18"
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		pv = p.ProtocolVersion // echo the client's negotiated version
	}
	return map[string]any{
		"protocolVersion": pv,
		"capabilities": map[string]any{
			"tools": map[string]any{},
			// This experimental key is what makes Claude Code treat us as a channel:
			"experimental": map[string]any{channelCapability: map[string]any{}},
		},
		"serverInfo": map[string]any{"name": "tgctl-claude-channel", "version": version},
		"instructions": "Telegram messages arrive as `notifications/claude/channel` with meta.chat_id and meta.user_id — " +
			"reply with the `reply` tool, passing that chat_id back. A button tap arrives with meta.kind=callback_query, " +
			"meta.data and meta.callback_query_id — you MUST call `answer_callback` with that id (Telegram spins on the button until " +
			"you do), then act on meta.data. Full toolbox: reply (text + optional inline `buttons` [[{text,callback_data|url}]], " +
			"parse_mode MarkdownV2/HTML, reply_to), react, edit, poll, photo, document, dice, pin, unpin, answer_callback. " +
			"Offer tappable choices with `buttons`; each tap comes back as a new turn.",
	}
}

func toolDefs() []map[string]any {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	strArr := map[string]any{"type": "array", "items": str}
	// buttons: an inline keyboard as rows of buttons; each button has text and one
	// of callback_data (a tap arrives back as a channel turn) or url (opens a link).
	buttons := map[string]any{
		"type":        "array",
		"description": "inline keyboard: array of rows; each row an array of {text, and callback_data OR url}",
		"items": map[string]any{"type": "array", "items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": str, "callback_data": str, "url": str},
			"required":   []string{"text"},
		}},
	}
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	return []map[string]any{
		{"name": "reply", "description": "Send a text message. Optional inline keyboard (buttons), Markdown/HTML (parse_mode: MarkdownV2|HTML), and reply_to a message_id.",
			"inputSchema": obj(map[string]any{"chat_id": str, "text": str, "buttons": buttons, "parse_mode": str, "reply_to": str}, "chat_id", "text")},
		{"name": "react", "description": "Set an emoji reaction on a message.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str, "emoji": str}, "chat_id", "message_id", "emoji")},
		{"name": "edit", "description": "Edit the text (and optional buttons) of a message you sent.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str, "text": str, "buttons": buttons}, "chat_id", "message_id", "text")},
		{"name": "poll", "description": "Send a native poll.",
			"inputSchema": obj(map[string]any{"chat_id": str, "question": str, "options": strArr, "is_anonymous": boolean, "allows_multiple_answers": boolean}, "chat_id", "question", "options")},
		{"name": "photo", "description": "Send a photo by URL, file_id, or local absolute path. Optional caption.",
			"inputSchema": obj(map[string]any{"chat_id": str, "photo": str, "caption": str}, "chat_id", "photo")},
		{"name": "document", "description": "Send a document/file by URL, file_id, or local absolute path. Optional caption.",
			"inputSchema": obj(map[string]any{"chat_id": str, "document": str, "caption": str}, "chat_id", "document")},
		{"name": "dice", "description": "Send an animated emoji with a random value: 🎲 🎯 🏀 ⚽ 🎳 🎰 (default 🎲).",
			"inputSchema": obj(map[string]any{"chat_id": str, "emoji": str}, "chat_id")},
		{"name": "pin", "description": "Pin a message in the chat.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str, "silent": boolean}, "chat_id", "message_id")},
		{"name": "unpin", "description": "Unpin a specific message, or the most recent pin if message_id is omitted.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str}, "chat_id")},
		{"name": "answer_callback", "description": "Answer a button tap (callback query): a toast, or an alert if show_alert is true. Always answer taps you receive.",
			"inputSchema": obj(map[string]any{"callback_query_id": str, "text": str, "show_alert": boolean}, "callback_query_id")},
	}
}

func (s *server) handleToolCall(m inMsg) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.out.send(rpcErr(m.ID, -32602, "invalid params: "+err.Error()))
		return
	}
	text, err := s.callTool(p.Name, p.Arguments)
	if err != nil {
		// Tool failures are reported in-band (isError) so the model sees them,
		// per the MCP tools spec.
		s.out.send(result(m.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		}))
		return
	}
	s.out.send(result(m.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}))
}

func (s *server) callTool(name string, args json.RawMessage) (string, error) {
	switch name {
	case "reply":
		var a struct {
			ChatID    string          `json:"chat_id"`
			Text      string          `json:"text"`
			Buttons   json.RawMessage `json:"buttons"`
			ParseMode string          `json:"parse_mode"`
			ReplyTo   string          `json:"reply_to"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		s.typing.stop(a.ChatID) // answer is going out — drop the "typing…" cue
		p := map[string]any{"chat_id": a.ChatID, "text": a.Text}
		if a.ParseMode != "" {
			p["parse_mode"] = a.ParseMode
		}
		if id, ok := asInt(a.ReplyTo); ok {
			p["reply_parameters"] = map[string]any{"message_id": id}
		}
		if kb := inlineKeyboard(a.Buttons); kb != nil {
			p["reply_markup"] = kb
		}
		return s.api("sendMessage", p)

	case "react":
		var a struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Emoji     string `json:"emoji"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		id, _ := asInt(a.MessageID)
		return s.api("setMessageReaction", map[string]any{
			"chat_id": a.ChatID, "message_id": id,
			"reaction": []map[string]any{{"type": "emoji", "emoji": a.Emoji}},
		})

	case "edit":
		var a struct {
			ChatID    string          `json:"chat_id"`
			MessageID string          `json:"message_id"`
			Text      string          `json:"text"`
			Buttons   json.RawMessage `json:"buttons"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		id, _ := asInt(a.MessageID)
		p := map[string]any{"chat_id": a.ChatID, "message_id": id, "text": a.Text}
		if kb := inlineKeyboard(a.Buttons); kb != nil {
			p["reply_markup"] = kb
		}
		return s.api("editMessageText", p)

	case "poll":
		var a struct {
			ChatID   string   `json:"chat_id"`
			Question string   `json:"question"`
			Options  []string `json:"options"`
			Anon     *bool    `json:"is_anonymous"`
			Multi    *bool    `json:"allows_multiple_answers"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		p := map[string]any{"chat_id": a.ChatID, "question": a.Question, "options": a.Options}
		if a.Anon != nil {
			p["is_anonymous"] = *a.Anon
		}
		if a.Multi != nil {
			p["allows_multiple_answers"] = *a.Multi
		}
		return s.api("sendPoll", p)

	case "photo":
		var a struct {
			ChatID  string `json:"chat_id"`
			Photo   string `json:"photo"`
			Caption string `json:"caption"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.sendMedia("photo", a.ChatID, a.Photo, a.Caption)

	case "document":
		var a struct {
			ChatID   string `json:"chat_id"`
			Document string `json:"document"`
			Caption  string `json:"caption"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.sendMedia("document", a.ChatID, a.Document, a.Caption)

	case "dice":
		var a struct {
			ChatID string `json:"chat_id"`
			Emoji  string `json:"emoji"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		p := map[string]any{"chat_id": a.ChatID}
		if a.Emoji != "" {
			p["emoji"] = a.Emoji
		}
		return s.api("sendDice", p)

	case "pin":
		var a struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Silent    *bool  `json:"silent"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		id, _ := asInt(a.MessageID)
		p := map[string]any{"chat_id": a.ChatID, "message_id": id}
		if a.Silent != nil {
			p["disable_notification"] = *a.Silent
		}
		return s.api("pinChatMessage", p)

	case "unpin":
		var a struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		p := map[string]any{"chat_id": a.ChatID}
		if id, ok := asInt(a.MessageID); ok {
			p["message_id"] = id
		}
		return s.api("unpinChatMessage", p)

	case "answer_callback":
		var a struct {
			ID        string `json:"callback_query_id"`
			Text      string `json:"text"`
			ShowAlert *bool  `json:"show_alert"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		p := map[string]any{"callback_query_id": a.ID}
		if a.Text != "" {
			p["text"] = a.Text
		}
		if a.ShowAlert != nil {
			p["show_alert"] = *a.ShowAlert
		}
		return s.api("answerCallbackQuery", p)

	default:
		return "", &toolError{op: "tools/call", detail: "unknown tool: " + name}
	}
}

// api sends a Bot API method with a JSON body through tgctl's generic escape hatch.
func (s *server) api(method string, payload map[string]any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.tg.cmd("api", method, "--data", string(data))
}

// sendMedia sends a photo/document. tgctl's media command accepts a URL, a file_id,
// or a local path (handling the multipart upload), so one path covers every source.
func (s *server) sendMedia(kind, chatID, src, caption string) (string, error) {
	args := []string{"media", kind, "--chat", chatID, "--" + kind, src}
	if caption != "" {
		args = append(args, "--caption", caption)
	}
	return s.tg.cmd(args...)
}

// inlineKeyboard turns the tool's rows-of-buttons into a reply_markup object, or nil.
func inlineKeyboard(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var rows [][]map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": rows}
}

func asInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}
