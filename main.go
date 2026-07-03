// Command tgctl-claude-channel is a Claude Code "channel": an MCP stdio server that
// bridges a Telegram bot to a Claude Code session. Inbound Telegram messages become
// notifications/claude/channel turns; the assistant replies through MCP tools. Every
// Telegram operation goes through the `tgctl` CLI, so this process reimplements no
// Bot-API logic — tgctl's 100+ verbs are the whole surface, for free.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "0.3.0"

// Config is the channel's runtime configuration, entirely from the environment so the
// channel stays a thin transport over tgctl.
type Config struct {
	TgctlBin       string   // path to the tgctl binary
	BotToken       string   // passed to tgctl as TGCTL_TOKEN; never logged
	AllowSeed      []string // user_ids to seed access.json's allowlist on first run
	StateDir       string   // access.json, inbox/, bot.pid, poll cursor live here
	OffsetFile     string   // getUpdates cursor
	CommandHandler string   // optional executable that handles recognized bot commands
}

func loadConfig() Config {
	stateDir := envOr("TGCTL_CHANNEL_STATE_DIR", defaultStateDir())
	return Config{
		TgctlBin:       envOr("TGCTL_BIN", "tgctl"),
		BotToken:       os.Getenv("TGCTL_TOKEN"),
		AllowSeed:      parseAllowList(os.Getenv("TGCTL_CHANNEL_ALLOW")),
		StateDir:       stateDir,
		OffsetFile:     envOr("TGCTL_CHANNEL_OFFSET_FILE", filepath.Join(stateDir, "poll-offset")),
		CommandHandler: os.Getenv("TGCTL_CHANNEL_COMMAND_HANDLER"),
	}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".tgctl-claude"
	}
	return filepath.Join(home, ".config", "tgctl-claude")
}

func parseAllowList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// --- transport: every Telegram write goes through tgctl -------------------------------

type transport interface {
	send(chatID, text string) (string, error)
	react(chatID, messageID, emoji string) (string, error)
	edit(chatID, messageID, text string) (string, error)
	action(chatID, action string) (string, error)
	// cmd runs an arbitrary tgctl invocation — the escape hatch that exposes the
	// full Telegram toolbox (polls, media, keyboards, file download, …).
	cmd(args ...string) (string, error)
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

func (t tgctlTransport) cmd(args ...string) (string, error) { return t.run(args...) }

func (t tgctlTransport) send(chatID, text string) (string, error) {
	return t.run("message", "send", "--chat", chatID, "--text", text)
}

func (t tgctlTransport) react(chatID, messageID, emoji string) (string, error) {
	data := `{"chat_id":` + chatID + `,"message_id":` + messageID +
		`,"reaction":[{"type":"emoji","emoji":` + strconv.Quote(emoji) + `}]}`
	return t.run("api", "setMessageReaction", "--data", data)
}

func (t tgctlTransport) edit(chatID, messageID, text string) (string, error) {
	data := `{"chat_id":` + chatID + `,"message_id":` + messageID + `,"text":` + strconv.Quote(text) + `}`
	return t.run("api", "editMessageText", "--data", data)
}

func (t tgctlTransport) action(chatID, action string) (string, error) {
	data := `{"chat_id":` + chatID + `,"action":` + strconv.Quote(action) + `}`
	return t.run("api", "sendChatAction", "--data", data)
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

// --- lifecycle ------------------------------------------------------------------------

func main() {
	log.SetPrefix("tgctl-claude-channel: ")
	log.SetFlags(0)

	cfg := loadConfig()
	if cfg.BotToken == "" {
		log.Fatal("TGCTL_TOKEN is required (the bot token tgctl uses)")
	}
	writePID(cfg.StateDir)

	tg := tgctlTransport{bin: cfg.TgctlBin, token: cfg.BotToken}
	ackVal, ackSet := os.LookupEnv("TGCTL_CHANNEL_ACK_REACTION")
	srv := &server{
		out:     newOut(os.Stdout),
		tg:      tg,
		typing:  newTypingManager(tg),
		store:   newAccessStore(cfg.StateDir, cfg.AllowSeed, ackVal, ackSet),
		perms:   newPermissionManager(),
		cfg:     cfg,
		botUser: fetchBotUsername(tg),
		cmdHook: loadCommandHook(cfg.CommandHandler),
	}
	srv.store.ensureSeed() // materialize access.json so the operator can edit it
	if srv.cmdHook != nil {
		go srv.cmdHook.registerCommands(tg) // publish the Telegram command menu
	}

	ctx, cancel := context.WithCancel(context.Background())
	installShutdown(cfg, cancel)
	go watchdog(cfg, cancel)

	go func() {
		if err := srv.runInbound(ctx); err != nil && ctx.Err() == nil {
			log.Printf("inbound stopped: %v", err)
		}
	}()

	// Blocks until Claude Code closes the MCP pipe (stdin EOF). Then clean up so we
	// don't leave a poller holding the bot token (a 409 for the next session).
	srv.serve(os.Stdin)
	shutdown(cfg, cancel)
}

// writePID records our PID and replaces a stale poller from a crashed session — only
// one getUpdates consumer per token is allowed, else every new session sees 409.
func writePID(stateDir string) {
	_ = os.MkdirAll(stateDir, 0o700)
	pidFile := filepath.Join(stateDir, "bot.pid")
	if b, err := os.ReadFile(pidFile); err == nil {
		if stale, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && stale > 1 && stale != os.Getpid() {
			if proc, e := os.FindProcess(stale); e == nil && proc.Signal(syscall.Signal(0)) == nil {
				log.Printf("replacing stale poller pid=%d", stale)
				_ = proc.Signal(syscall.SIGTERM)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func installShutdown(cfg Config, cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sig
		shutdown(cfg, cancel)
	}()
}

// watchdog self-terminates if we're orphaned (parent chain severed by a crash), so a
// dead session never leaves us polling forever.
func watchdog(cfg Config, cancel context.CancelFunc) {
	boot := os.Getppid()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if os.Getppid() != boot {
			shutdown(cfg, cancel)
			return
		}
	}
}

var shutdownOnce = make(chan struct{}, 1)

func shutdown(cfg Config, cancel context.CancelFunc) {
	select {
	case shutdownOnce <- struct{}{}:
	default:
		return // already shutting down
	}
	cancel() // cancels the poll context → kills the in-flight tgctl subprocess
	pidFile := filepath.Join(cfg.StateDir, "bot.pid")
	if b, err := os.ReadFile(pidFile); err == nil {
		if p, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && p == os.Getpid() {
			_ = os.Remove(pidFile)
		}
	}
	// Give the cancelled subprocess a moment to die, then exit.
	go func() { time.Sleep(time.Second); os.Exit(0) }()
	os.Exit(0)
}

// fetchBotUsername resolves the bot's @username (needed for group mention detection).
// Best-effort — an empty result just means group-mention matching falls back to
// text_mention / reply-to-bot / configured patterns.
func fetchBotUsername(tg transport) string {
	out, err := tg.cmd("bot", "info", "-o", "json")
	if err != nil {
		return ""
	}
	var me struct {
		Username string `json:"username"`
	}
	if json.Unmarshal([]byte(out), &me) != nil {
		return ""
	}
	return me.Username
}
