// Command tgctl-claude-channel is a Claude Code "channel": an MCP stdio server
// that bridges a Telegram bot to a Claude Code session. Inbound Telegram
// messages become `notifications/claude/channel` turns; the assistant replies
// through MCP tools. All Telegram I/O goes through the `tgctl` CLI, so this
// process owns no Bot-API logic and no secrets — tgctl holds the token.
package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var version = "0.1.0"

// Config is the channel's runtime configuration, entirely from the environment
// so the channel stays a thin transport over tgctl.
type Config struct {
	TgctlBin string         // path to the tgctl binary
	BotToken string         // passed to tgctl as TGCTL_TOKEN; never logged
	Allow    map[int64]bool // allowlisted Telegram sender user_ids — the security gate
	Port     string         // local port tgctl's webhook receiver listens on
	Secret   string         // X-Telegram-Bot-Api-Secret-Token tgctl enforces
	SetURL   string         // optional public HTTPS URL to register the webhook at
}

func loadConfig() Config {
	return Config{
		TgctlBin: envOr("TGCTL_BIN", "tgctl"),
		BotToken: os.Getenv("TGCTL_TOKEN"),
		Port:     envOr("TGCTL_CHANNEL_PORT", "8080"),
		Secret:   os.Getenv("TGCTL_CHANNEL_SECRET"),
		SetURL:   os.Getenv("TGCTL_CHANNEL_SET_URL"),
		Allow:    parseAllow(os.Getenv("TGCTL_CHANNEL_ALLOW")),
	}
}

func parseAllow(s string) map[int64]bool {
	m := map[int64]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			m[id] = true
		}
	}
	return m
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// transport is the outbound side: every Telegram write goes through tgctl, so
// the channel reuses tgctl's auth and never calls the Bot API itself.
type transport interface {
	send(chatID, text string) (string, error)
	react(chatID, messageID, emoji string) (string, error)
	edit(chatID, messageID, text string) (string, error)
}

type tgctlTransport struct {
	bin   string
	token string
}

func (t tgctlTransport) run(args ...string) (string, error) {
	cmd := exec.Command(t.bin, args...)
	cmd.Env = append(os.Environ(), "TGCTL_TOKEN="+t.token)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return "", &toolError{op: args[0], detail: s, err: err}
	}
	return s, nil
}

func (t tgctlTransport) send(chatID, text string) (string, error) {
	return t.run("message", "send", "--chat", chatID, "--text", text)
}

// react/edit use tgctl's generic `api` escape hatch so we don't depend on
// specific subcommand flag shapes that may evolve.
func (t tgctlTransport) react(chatID, messageID, emoji string) (string, error) {
	data := `{"chat_id":` + chatID + `,"message_id":` + messageID +
		`,"reaction":[{"type":"emoji","emoji":` + strconv.Quote(emoji) + `}]}`
	return t.run("api", "setMessageReaction", "--data", data)
}

func (t tgctlTransport) edit(chatID, messageID, text string) (string, error) {
	data := `{"chat_id":` + chatID + `,"message_id":` + messageID +
		`,"text":` + strconv.Quote(text) + `}`
	return t.run("api", "editMessageText", "--data", data)
}

type toolError struct {
	op     string
	detail string
	err    error
}

func (e *toolError) Error() string {
	if e.detail != "" {
		return "tgctl " + e.op + ": " + e.detail
	}
	return "tgctl " + e.op + ": " + e.err.Error()
}

func main() {
	log.SetPrefix("tgctl-claude-channel: ")
	log.SetFlags(0)

	cfg := loadConfig()
	if cfg.BotToken == "" {
		log.Fatal("TGCTL_TOKEN is required (the bot token tgctl uses)")
	}
	// Fail closed: an ungated channel would let anyone who finds the bot drive
	// the agent. Require an explicit sender allowlist.
	if len(cfg.Allow) == 0 {
		log.Fatal("TGCTL_CHANNEL_ALLOW is required (comma-separated allowlisted user_ids); refusing to run an open channel")
	}

	srv := &server{
		out: newOut(os.Stdout),
		tg:  tgctlTransport{bin: cfg.TgctlBin, token: cfg.BotToken},
	}

	// Inbound pump (Telegram → session) runs alongside the stdio request loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.runInbound(ctx, cfg); err != nil {
			log.Printf("inbound stopped: %v", err)
		}
	}()

	// Request loop: read JSON-RPC from Claude Code (stdin), dispatch.
	srv.serve(os.Stdin)
}
