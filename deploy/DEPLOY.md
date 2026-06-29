# Deploying the channel on the VPS

Goal: a persistent Claude Code session on the VPS, driven from Telegram via this
channel, on a **bot separate from Hermes** (so the two never contend for the same
`getUpdates`/webhook stream).

> Channels are a Claude Code **research preview** (`--dangerously-load-development-channels`).
> The API may change; treat this as experimental.

## 0. Prereqs (one-time)
- `tgctl` installed on the VPS and on `PATH` (`TGCTL_BIN`).
- `claude` (Claude Code) installed on the VPS.
- A bot that is **not** Hermes's bot. The shared `ElPapehAssistantBot` token was
  exposed in chat — **rotate it** in @BotFather (`/revoke`) and use the new one.

## 1. Build / copy the binary
On the Mac (cross-compile) then copy, or build on the VPS:
```sh
# from a checkout, for an amd64 VPS:
GOOS=linux GOARCH=amd64 go build -o tgctl-claude-channel .
scp tgctl-claude-channel VPS:/usr/local/bin/tgctl-claude-channel
# or on the VPS:  go build -o /usr/local/bin/tgctl-claude-channel .
```

## 2. Cloudflare tunnel (webhook ingress)  → needs Juan
Expose `TGCTL_CHANNEL_PORT` over HTTPS via `cloudflared`, e.g.
`claude-channel.jjuanrivvera.com` → `http://localhost:8090`. Put that URL in
`TGCTL_CHANNEL_SET_URL`. The origin port stays private; only Cloudflare's edge is
public. Telegram requires a public HTTPS endpoint — see the security model below.

## 3. Channel env
```sh
sudo install -d -m 700 /etc/tgctl-claude
sudo cp deploy/channel.env.example /etc/tgctl-claude/channel.env
sudo chmod 600 /etc/tgctl-claude/channel.env
sudoedit /etc/tgctl-claude/channel.env   # set token, allow=1478765505, secret, set-url
```

## 4. MCP config
```sh
cp deploy/mcp.json.example ~/Assistant/.mcp.json   # adjust the binary path
```

## 5. Authenticate claude  → needs Juan (interactive)
```sh
claude /login    # the claude.ai subscription login — flat-cost, the whole point
```

## 6. Run it (pick one)
**systemd** (auto-restart):
```sh
sudo cp deploy/tgctl-claude.service /etc/systemd/system/   # set User/WorkingDirectory
sudo systemctl daemon-reload && sudo systemctl enable --now tgctl-claude
journalctl -u tgctl-claude -f
```
**tmux** (use this if claude needs a TTY in channel mode — the proven path):
```sh
tmux new -s claude-tg
set -a; . /etc/tgctl-claude/channel.env; set +a
cd ~/Assistant && claude --dangerously-load-development-channels server:tgctl-claude-channel --dangerously-skip-permissions
```

## 7. Verify
- `/start` the bot from your Telegram (a bot can't initiate a chat).
- Send it a message → it should arrive in the session as a `<channel>` turn and
  the assistant should `reply`.
- Non-allowlisted senders and POSTs without the secret token are dropped.

## Security model
Telegram delivers to a **public** HTTPS endpoint; defense is layered:
**secret token** (rejects forged POSTs) + **Cloudflare Tunnel** (origin not exposed)
+ optional **Telegram IP ranges** at the proxy + **sender allowlist** (only your
`user_id` drives the agent). A message ultimately drives an agent with permissions
— the allowlist + secret token are non-negotiable.
