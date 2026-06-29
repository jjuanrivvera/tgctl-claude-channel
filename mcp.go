package main

import (
	"encoding/json"
	"io"
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
	out *out
	tg  transport
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
		"instructions": "Telegram messages arrive as `notifications/claude/channel` with " +
			"meta.chat_id and meta.user_id. Respond by calling the `reply` tool with that chat_id. " +
			"Use `react` to acknowledge a message and `edit` to update one you sent.",
	}
}

func toolDefs() []map[string]any {
	str := map[string]any{"type": "string"}
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	return []map[string]any{
		{
			"name":        "reply",
			"description": "Send a text message to a Telegram chat.",
			"inputSchema": obj(map[string]any{"chat_id": str, "text": str}, "chat_id", "text"),
		},
		{
			"name":        "react",
			"description": "Set an emoji reaction on a Telegram message.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str, "emoji": str}, "chat_id", "message_id", "emoji"),
		},
		{
			"name":        "edit",
			"description": "Edit the text of a message you previously sent.",
			"inputSchema": obj(map[string]any{"chat_id": str, "message_id": str, "text": str}, "chat_id", "message_id", "text"),
		},
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
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.tg.send(a.ChatID, a.Text)
	case "react":
		var a struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Emoji     string `json:"emoji"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.tg.react(a.ChatID, a.MessageID, a.Emoji)
	case "edit":
		var a struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.tg.edit(a.ChatID, a.MessageID, a.Text)
	default:
		return "", &toolError{op: "tools/call", detail: "unknown tool: " + name}
	}
}
