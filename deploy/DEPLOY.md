# Deploying the channel on the VPS

Goal: a persistent Claude Code session on the VPS, driven from Telegram via this
channel, on a **bot separate from any other agent** (so two consumers never contend
for the same `getUpdates` stream).

> Channels are a Claude Code **research preview** (`--dangerously-load-development-channels`).
> The API may change; treat this as experimental.

The default transport is **long-poll** (`tgctl updates get`) — no webhook, no public
endpoint, no tunnel. The box reaches out to Telegram, which sidesteps Cloudflare Bot
Fight Mode blocking public POSTs.

## 0. Prereqs (one-time)
- `tgctl` installed on the VPS and on `PATH` (or point `TGCTL_BIN` at it).
- `claude` (Claude Code) installed and authenticated on the VPS (`claude /login`).
- A bot that no other agent uses. Rotate the token in @BotFather if it was ever exposed.

## 1. Build / deploy the binary
Cross-compile on your Mac, then copy to a **user-writable** path (no sudo, and future
redeploys are a one-liner):
```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o tgctl-claude-channel .
# copy with mv-over-inode so a running binary doesn't error "Text file busy":
ssh VPS 'cat > ~/bin/tgctl-claude-channel.new && chmod +x ~/bin/tgctl-claude-channel.new \
         && mv -f ~/bin/tgctl-claude-channel.new ~/bin/tgctl-claude-channel' < tgctl-claude-channel
```

## 2. Channel env  (must be EXPORTED at launch)
```sh
install -d -m 700 ~/.config/tgctl-claude
cp deploy/channel.env.example ~/.config/tgctl-claude/channel.env
chmod 600 ~/.config/tgctl-claude/channel.env
$EDITOR ~/.config/tgctl-claude/channel.env    # set TGCTL_TOKEN, TGCTL_CHANNEL_ALLOW=<your user_id>
```
The webhook-mode vars (`_PORT`/`_SECRET`/`_SET_URL`) are legacy and unused by polling.

## 3. MCP config — exactly ONE source
Register the server in the project `.mcp.json` **and keep the plugin disabled**. Two
registrations (plugin + `.mcp.json`) spawn two racing processes.
```jsonc
// ~/Assistant/.mcp.json
{ "mcpServers": { "tgctl-claude-channel": { "command": "/home/you/bin/tgctl-claude-channel" } } }
```

## 4. Run it — tmux (the proven path)
Channel mode needs a TTY, and the dev flag prompts once per launch, so run it in tmux.
`deploy/channel-up.sh` (or `scripts/channel-up.sh`) does the whole dance headlessly:
resets stale state, launches detached, **auto-answers the prompts**, and verifies.
```sh
bash channel-up.sh
```
Or by hand:
```sh
tmux new -s claude-tg
set -a; source ~/.config/tgctl-claude/channel.env; set +a   # export is required
cd ~/Assistant
claude --dangerously-load-development-channels server:tgctl-claude-channel
#   → answer the "development channels" warning with option 1 (proceed)
#   → the flag takes the entry DIRECTLY; no separate --channels, no --mcp-config
#   → --dangerously-skip-permissions is OPTIONAL: the channel relays tool-approval
#     prompts to Telegram (Allow/Deny buttons), so you can leave the sandbox ON.
```

Reboot-survival: a `systemd` oneshot (`Type=oneshot`, `RemainAfterExit=yes`,
`KillMode=none`) that runs `channel-up.sh` at boot; enable `loginctl enable-linger`
so the tmux server survives without a login session.

## 5. Verify
- Send the bot a message → it arrives as a channel turn; you get a `👀` reaction, a
  "typing…" indicator, then a reply. Non-allowlisted senders are dropped.
- Ask it for a **poll** or **buttons**; tap a button → it comes back as a turn.
- Health: the MCP log (`~/.cache/claude-cli-nodejs/*/mcp-logs-tgctl-claude-channel/*.jsonl`)
  shows `Channel notifications registered`; `pgrep -f "tgctl updates get"` is the poller.

## Troubleshooting (hard-won)
- **Server connects but nothing injects / replies fail** → the env wasn't exported;
  the server has no token. Relaunch with `set -a; source …; set +a`.
- **Two channel processes / flaky activation** → the plugin *and* `.mcp.json` both
  register it. Disable the plugin; keep one source.
- **`server:<name>` = "no MCP server configured with that name"** → the server isn't in
  the MCP config for that cwd, or `--mcp-config` pointed elsewhere. Register it in the
  project `.mcp.json`.
- **Real Telegram messages never arrive, but local tests do** → you were on the webhook
  transport and Cloudflare Bot Fight Mode 403'd the POSTs. Use the default long-poll.

## Security model
Long-poll has **no public endpoint** — the box connects out to Telegram. Defense is the
**sender allowlist** (only your `user_id` drives the agent; fails closed) plus `tgctl`
owning the token (this process never touches the Bot API). A message ultimately drives an
agent with permissions — the allowlist is non-negotiable.
