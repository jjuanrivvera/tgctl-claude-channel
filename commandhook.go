package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// commandHook routes recognized bot commands to an external handler executable instead of
// relaying them into the session as a turn. The handler owns both the command set (via its
// `list` subcommand) and the behavior (via `run`), so the channel stays generic: it knows
// how to invoke a handler and relay its stdout, and nothing about what any command does.
//
// Contract with the handler executable:
//
//	<handler> list             -> JSON [{"command":"model","description":"…"}, …] on stdout
//	<handler> run <cmd> <args> -> performs the command; stdout is relayed to the chat.
//	                              Message metadata is passed in the environment (TG_* vars),
//	                              so a handler can also talk to Telegram directly (buttons,
//	                              code blocks, media) via tgctl and emit no stdout.
//
// This is the escape hatch that lets an operator wire up privileged, deployment-specific
// commands (e.g. driving the host Claude Code REPL) without any of that logic living here.
type commandHook struct {
	bin      string
	commands []hookCommand
	byName   map[string]bool
	timeout  time.Duration
}

type hookCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// loadCommandHook probes the handler's command manifest once at startup. It returns nil when
// the feature is unconfigured or the probe fails — a missing or broken handler disables the
// feature, it never blocks the channel from starting.
func loadCommandHook(bin string) *commandHook {
	if strings.TrimSpace(bin) == "" {
		return nil
	}
	out, err := exec.Command(bin, "list").Output()
	if err != nil {
		return nil
	}
	var cmds []hookCommand
	if json.Unmarshal(out, &cmds) != nil {
		return nil
	}
	h := &commandHook{bin: bin, byName: map[string]bool{}, timeout: 45 * time.Second}
	for _, c := range cmds {
		name := strings.ToLower(strings.TrimSpace(c.Command))
		if name == "" || h.byName[name] {
			continue
		}
		h.byName[name] = true
		h.commands = append(h.commands, hookCommand{Command: name, Description: c.Description})
	}
	if len(h.commands) == 0 {
		return nil
	}
	return h
}

// handles reports whether name is one of the handler's commands. Safe on a nil hook so the
// call site stays a one-liner whether or not the feature is configured.
func (h *commandHook) handles(name string) bool {
	return h != nil && h.byName[strings.ToLower(name)]
}

// run invokes `<handler> run <cmd> <args>` and relays its stdout to the chat as a reply.
// Message metadata rides the environment so handlers may also reach Telegram directly.
func (h *commandHook) run(s *server, m *message, chatID, name, args string) {
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, h.bin, "run", name, args)
	c.Env = append(os.Environ(),
		"TG_CHAT_ID="+chatID,
		"TG_USER_ID="+strconv.FormatInt(m.From.ID, 10),
		"TG_MESSAGE_ID="+strconv.FormatInt(m.MessageID, 10),
		"TG_COMMAND="+name,
		"TG_ARGS="+args,
		"TG_TEXT="+m.textOrCaption(),
	)
	out, err := c.Output()
	if err != nil {
		detail := strings.TrimSpace(stderrOf(err))
		if detail == "" {
			detail = err.Error()
		}
		_, _ = s.tg.send(chatID, "⚠️ /"+name+" failed: "+detail)
		return
	}
	if reply := strings.TrimSpace(string(out)); reply != "" {
		_, _ = s.reply(chatID, reply, "", "", nil, nil)
	}
}

// handlerCallbackPrefix namespaces inline-button taps a handler owns. A tap with this prefix
// is routed to the handler (which drives the host session) instead of the model, so a handler
// can offer interactive keyboards — e.g. a native model/effort picker — and act on the choice.
const handlerCallbackPrefix = "hnd:"

// ownsCallback reports whether a button tap belongs to the handler. Nil-safe.
func (h *commandHook) ownsCallback(data string) bool {
	return h != nil && strings.HasPrefix(data, handlerCallbackPrefix)
}

// callback invokes `<handler> callback <data>` on a button tap the handler owns. The handler
// answers the callback and updates its message via tgctl itself; on failure we answer here so
// Telegram's button spinner stops. Message coordinates and the callback id ride the env.
func (h *commandHook) callback(s *server, cq *callbackQuery) {
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, h.bin, "callback", cq.Data)
	var chatID, msgID string
	if cq.Message != nil {
		chatID = strconv.FormatInt(cq.Message.Chat.ID, 10)
		msgID = strconv.FormatInt(cq.Message.MessageID, 10)
	}
	c.Env = append(os.Environ(),
		"TG_CHAT_ID="+chatID,
		"TG_MESSAGE_ID="+msgID,
		"TG_CALLBACK_ID="+cq.ID,
		"TG_USER_ID="+strconv.FormatInt(cq.From.ID, 10),
		"TG_DATA="+cq.Data,
	)
	out, err := c.Output()
	if err != nil {
		_, _ = s.tg.cmd("callback", "answer", "--callback-query-id", cq.ID, "--text", "failed")
		return
	}
	if reply := strings.TrimSpace(string(out)); reply != "" {
		_, _ = s.reply(chatID, reply, "", "", nil, nil)
	}
}

// registerCommands publishes the bot's command menu — the handler's commands plus the
// built-in pairing commands — so Telegram shows autocomplete. Best-effort.
func (h *commandHook) registerCommands(tg transport) {
	cmds := []map[string]string{
		{"command": "start", "description": "Pairing instructions"},
		{"command": "help", "description": "How this bot works"},
		{"command": "status", "description": "Check pairing state"},
	}
	for _, c := range h.commands {
		desc := c.Description
		if desc == "" {
			desc = "/" + c.Command
		}
		cmds = append(cmds, map[string]string{"command": c.Command, "description": truncateRunes(desc, 256)})
	}
	data, err := json.Marshal(map[string]any{"commands": cmds})
	if err != nil {
		return
	}
	_, _ = tg.cmd("api", "setMyCommands", "--data", string(data))
}

// stderrOf extracts captured stderr from a failed exec so command failures surface a useful
// message instead of a bare "exit status 1".
func stderrOf(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return string(ee.Stderr)
	}
	return ""
}

// commandArgs returns everything after the leading "/cmd" (or "/cmd@bot") token.
func commandArgs(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexAny(t, " \t\n"); i >= 0 {
		return strings.TrimSpace(t[i+1:])
	}
	return ""
}

// truncateRunes caps s at n runes (Telegram command descriptions max out at 256).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
