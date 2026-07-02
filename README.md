# tgctl-claude-channel

A [Claude Code **channel**](https://docs.claude.com/en/docs/claude-code) that bridges a Telegram bot to a Claude Code session — drive a persistent agent from your phone, with a full interactive toolbox and permission approvals on the device.

It's a small **MCP stdio server**: when you message the bot, it forwards the message to your Claude Code session; the assistant replies, reacts, sends polls, buttons, media, and asks for tool permissions — all through Telegram. Every Telegram operation goes through the [`tgctl`](https://github.com/jjuanrivvera/tgctl) CLI, so this process reimplements no Bot-API logic and never holds the token itself.

```
        Telegram · your bot
             │ ▲
   inbound   │ │  outbound (reply/poll/photo/react/…)
  (long-poll)│ │  + permission prompts
             ▼ │
┌──────────────────────────────────────────────┐
│  tgctl-claude-channel  (MCP stdio server)      │
│   • gate on sender (pairing / allowlist)       │
│   • inbound  → notifications/claude/channel     │─┐  every call
│   • tools    → tgctl (message/media/api)        │ │  runs through
│   • permission relay → Allow/Deny on Telegram   │─┘  the tgctl CLI
└──────────────────────────────────────────────┘
             │
             ▼
        Claude Code session
```

## Features

- **Two-way messaging.** Text and media arrive as channel turns; the assistant replies with text, inline **buttons**, **polls**, **dice**, photos, documents, reactions, edits and pins.
- **Interactive buttons.** Offer tappable choices; a tap comes back as a turn, so you can build menus and confirmations.
- **Attachments.** Inbound photos are downloaded so the assistant can read them; documents, voice, audio and video are fetched on demand.
- **Permission approvals on your phone.** When the assistant wants to use a tool, you get an **Allow / Deny** prompt in Telegram — so it runs with its permission sandbox on, no unsafe flags required.
- **Access control.** Pairing (no numeric IDs to copy), an allowlist to lock down, and per-group mention gating.
- **Presence.** A “seen” reaction on receipt and a live “typing…” indicator while the assistant works.

## Quick setup

1. **Create a bot** with [@BotFather](https://t.me/BotFather) (`/newbot`) and copy the token.
2. **Install `tgctl`** and authenticate it with that token (`tgctl auth login`).
3. **Register this server** in your project's `.mcp.json`:
   ```jsonc
   { "mcpServers": { "tgctl-claude-channel": { "command": "/path/to/tgctl-claude-channel" } } }
   ```
4. **Start Claude Code with the channel** (export the config first):
   ```sh
   export TGCTL_TOKEN=123456789:AA...          # the bot token tgctl uses
   claude --dangerously-load-development-channels server:tgctl-claude-channel
   ```
5. **Pair.** DM your bot — it replies with a 6-character code. In Claude Code, add yourself to the allowlist with that code, then message the bot again; it reaches the assistant.

For an always-on VPS deployment (systemd, headless launch), see [`deploy/DEPLOY.md`](deploy/DEPLOY.md).

## Configuration

| Var | Required | Meaning |
|---|---|---|
| `TGCTL_TOKEN` | yes | Bot token, passed to `tgctl` (never logged). |
| `TGCTL_CHANNEL_ALLOW` | no | Comma-separated Telegram `user_id`s to seed the allowlist on first run. |
| `TGCTL_CHANNEL_STATE_DIR` | no | Where `access.json`, the inbox and the poll cursor live (default `~/.config/tgctl-claude`). |
| `TGCTL_CHANNEL_ACK_REACTION` | no | Emoji reaction set on receipt (default `👀`; set empty to disable). |
| `TGCTL_BIN` | no | Path to the `tgctl` binary (default `tgctl`). |

## Tools exposed to the assistant

| Tool | Purpose |
|---|---|
| `reply` | Send text (auto-split past Telegram's limit). Optional inline **buttons**, file attachments (images as photos, others as documents), `parse_mode` and `reply_to`. |
| `react` / `edit` | React with an emoji; edit a message the bot sent (progress updates). |
| `poll` | Send a native poll. |
| `photo` / `document` | Send media by URL, `file_id`, or local path. |
| `dice`, `pin`, `unpin` | Animated dice; pin/unpin a message. |
| `answer_callback` | Answer a button tap (a toast or an alert). |
| `download_attachment` | Fetch an inbound attachment by `file_id` to the local inbox, ready to Read. |

## Access control

Inbound messages are gated on the **sender's `user_id`** — everything else is dropped before a turn is ever emitted. Three policies:

- **`pairing`** (default): an unknown DM gets a one-time code; the operator approves it to add the sender to the allowlist.
- **`allowlist`**: only listed `user_id`s get through; strangers are dropped silently.
- **Groups**: opt in per group, with an optional per-group allowlist and mention requirement (the bot answers only when addressed).

State lives in `access.json` under the state dir. Outbound tools are gated too — the assistant can only send to chats it may receive from.

## Why route through `tgctl`?

The bot's entire capability set — polls, dice, inline keyboards, media, file download, reactions, pins, callback answers — is just [`tgctl`](https://github.com/jjuanrivvera/tgctl) commands. This channel builds the request and shells out; `tgctl` owns the Bot-API surface, the credential (OS keyring) and the retries. So the channel is a thin, single static Go binary (no runtime to install), the full Telegram toolbox comes essentially for free, and any verb `tgctl` gains is instantly available here.

## Security model

Long-poll means **no public endpoint** — the process connects out to Telegram, so there's no webhook to attack. Defense rests on the **sender allowlist** (fails closed — it never runs open) plus `tgctl` owning the token (this process never touches the Bot API directly). Tool approvals are relayed to the operator's device, so the agent keeps its permission sandbox.

## Development

```sh
make verify   # gofmt + go vet + golangci-lint + tests (race) + coverage floor
make hooks    # install the pre-commit hook
```

MIT licensed.
