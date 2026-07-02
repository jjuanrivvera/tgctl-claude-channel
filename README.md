# tgctl-claude-channel

A [Claude Code **channel**](https://docs.claude.com/en/docs/claude-code) that bridges a Telegram bot to a Claude Code session — so you can drive a persistent agent from your phone, with a **full interactive toolbox** (buttons, polls, media, reactions).

It is a small **MCP stdio server** that speaks the Claude Code `claude/channel` contract. All Telegram I/O goes through the [`tgctl`](https://github.com/jjuanrivvera/tgctl) CLI, so this process holds **no Bot-API logic and no token** — `tgctl` owns the credential.

```
        Telegram · your bot
             │ ▲
   inbound   │ │  outbound (reply/poll/photo/react/… tools)
  (long-poll)│ │
             ▼ │
┌──────────────────────────────────────────────┐
│  tgctl-claude-channel  (MCP stdio server)      │
│   initialize → advertises experimental         │
│                claude/channel                  │
│   inbound:  tgctl updates get -o json  ────────┼─┐  uses tgctl as
│   gate on sender user_id (allowlist)           │ │  transport; tgctl
│   emit  notifications/claude/channel           │ │  holds the token
│   tools → tgctl (message/media/api)  ──────────┼─┘
└──────────────────────────────────────────────┘
             │
             ▼
        Claude Code session
```

## How it works

- **Inbound** (Telegram → session): **long-polls** `tgctl updates get` (no webhook, no public endpoint, no tunnel — the box reaches out to Telegram). It **gates each update on the sender's `user_id`** (the security boundary) and pushes accepted **messages** and **button taps** (`callback_query`) into the session as a `notifications/claude/channel` turn with `meta.chat_id` / `meta.user_id` (and `meta.callback_query_id` / `meta.data` for taps). The getUpdates cursor is persisted so restarts neither replay nor drop.
- **Outbound** (session → Telegram): exposes a full toolbox as MCP tools —
  `reply` (text + inline **buttons**, parse_mode, reply_to), `react`, `edit`, `poll`,
  `photo`, `document`, `dice`, `pin`, `unpin`, `answer_callback`. Each shells out to `tgctl`.
- **UX**: on an inbound message the channel sets a **`👀` reaction** ("seen") and keeps a
  live **"typing…"** indicator running until the reply lands.

## Build

```sh
make verify   # build + vet + test
make build    # -> bin/tgctl-claude-channel
```

## Configure (environment)

| Var | Required | Meaning |
|---|---|---|
| `TGCTL_TOKEN` | yes | Bot token, passed to the `tgctl` subprocesses (never logged) |
| `TGCTL_CHANNEL_ALLOW` | yes | Comma-separated allowlisted Telegram `user_id`s. **Fails closed** — refuses to start empty |
| `TGCTL_BIN` | no | Path to the `tgctl` binary (default `tgctl`) |
| `TGCTL_CHANNEL_ACK_REACTION` | no | Emoji reaction set on inbound messages as a "seen" cue (default `👀`; empty disables) |
| `TGCTL_CHANNEL_OFFSET_FILE` | no | Where the getUpdates cursor is persisted (default `~/.config/tgctl-claude/poll-offset`) |
| `TGCTL_CHANNEL_PORT` / `_SECRET` / `_SET_URL` | no | **Legacy (webhook mode)** — unused by the default long-poll transport |

> **Env must be exported.** The channel reads config from the *process environment*, and
> Claude Code passes its environment to the MCP server. So `source` your env with
> auto-export: `set -a; source channel.env; set +a` — a plain `source` of `KEY=val` lines
> leaves them as shell-locals and the server starts with **no token**.

## Wire into Claude Code

Channels are a research preview. The **plugin** form (`--channels plugin:…@…`) is still
gated by an org-level allowlist, so on a personal plan use the **dev-channel `server:`
form**, which loads a *manually configured* MCP server without that allowlist:

```jsonc
// project .mcp.json — one source only (do NOT also enable the plugin, or two
// instances of the server race and the channel won't bind cleanly)
{ "mcpServers": { "tgctl-claude-channel": { "command": "/path/to/bin/tgctl-claude-channel" } } }
```

```sh
set -a; source channel.env; set +a
claude --dangerously-load-development-channels server:tgctl-claude-channel --dangerously-skip-permissions
# answer the one-time "development channels" prompt with option 1 (proceed)
```

- The flag takes the entry **directly** (`… server:tgctl-claude-channel`), not via a
  separate `--channels`, and **no `--mcp-config`** (that would register a *second* server).
- `--dangerously-skip-permissions` is needed for autonomous replies.
- The dev flag prompts once per launch → drive it under `tmux` and auto-answer for an
  always-on deployment (see `deploy/DEPLOY.md`, `channel-up.sh`).

## Security model

Long-poll means **no public endpoint** — the box connects *out* to Telegram, so there is
no webhook to attack and nothing to sit behind Cloudflare. Defense reduces to:

1. **Sender allowlist** — only configured `user_id`s can drive the agent; every other
   update is dropped before any notification is emitted. The channel **fails closed**.
2. **`tgctl` owns the token** — this process never touches the Bot API or the credential.

(A webhook transport also existed; it needs a secret token + a tunnel and can be blocked
by Cloudflare Bot Fight Mode on a public POST — which is why polling is the default.)

## Gotchas learned in production

- **Cloudflare Bot Fight Mode (free plan) blocks Telegram webhook POSTs** (403 "Attention
  Required") and can't be excepted per-hostname → the long-poll transport sidesteps it.
- **One source for the MCP server.** If both a plugin *and* a project `.mcp.json` register
  `tgctl-claude-channel`, two processes race for the same work — keep exactly one.
- **Export the env** (`set -a`) or the server starts tokenless and connects but fails.
- Deploy over a running binary with `cp` fails ("Text file busy") — write `.new` then `mv`.
