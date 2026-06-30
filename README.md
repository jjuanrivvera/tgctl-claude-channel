# tgctl-claude-channel

A [Claude Code **channel**](https://docs.claude.com/en/docs/claude-code) that bridges a Telegram bot to a Claude Code session — so you can drive a persistent agent from your phone.

It is a small **MCP stdio server** that speaks the Claude Code `claude/channel` contract. All Telegram I/O goes through the [`tgctl`](https://github.com/jjuanrivvera/tgctl) CLI, so this process holds **no Bot-API logic and no token** — `tgctl` owns the credential.

```
        Telegram · your bot
             │ ▲
   inbound   │ │  outbound (reply/react/edit tools)
             ▼ │
┌──────────────────────────────────────────────┐
│  tgctl-claude-channel  (MCP stdio server)      │
│   initialize → advertises experimental         │
│                claude/channel                  │
│   inbound:  tgctl webhook listen -o json  ─────┼─┐  uses tgctl as
│   gate on sender user_id (allowlist)           │ │  transport; tgctl
│   emit  notifications/claude/channel           │ │  holds the token
│   reply tool → tgctl message send  ────────────┼─┘
└──────────────────────────────────────────────┘
             │
             ▼
        Claude Code session
```

## How it works

- **Inbound** (Telegram → session): runs `tgctl webhook listen -o json` as a subprocess, reads its JSON `Update` stream, **gates each message on the sender's `user_id`** (the security boundary), and pushes accepted messages into the session as a `notifications/claude/channel` notification. In Claude they surface as a channel turn with `meta.chat_id` / `meta.user_id`.
- **Outbound** (session → Telegram): exposes MCP tools `reply`, `react`, `edit`. Each shells out to `tgctl` (`message send`, or the `api` escape hatch). The assistant replies by calling `reply` with the `chat_id` from the notification's meta.

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
| `TGCTL_CHANNEL_PORT` | no | Local port for `tgctl webhook listen` (default `8080`) |
| `TGCTL_CHANNEL_SECRET` | no | `X-Telegram-Bot-Api-Secret-Token` enforced on inbound webhooks |
| `TGCTL_CHANNEL_SET_URL` | no | Public HTTPS URL to register the webhook at on startup |
| `TGCTL_BIN` | no | Path to the `tgctl` binary (default `tgctl`) |

## Wire into Claude Code

Channels are a research preview. Two ways to load it:

**As a plugin (production — no per-launch prompt, systemd-friendly).** This repo is also a
single-plugin marketplace; the bundled `.mcp.json` pulls the config from the *process
environment* via `${VAR}` (so no secrets live in the repo — set them with a systemd
`EnvironmentFile`). Inside a Claude Code session:

```
/plugin marketplace add jjuanrivvera/tgctl-claude-channel
/plugin install tgctl-claude-channel@jjuanrivvera
```

Then run the channel session (needs `tgctl-claude-channel` + `tgctl` on `PATH`):

```sh
claude --channels plugin:tgctl-claude-channel@jjuanrivvera --dangerously-skip-permissions
```

**As a dev channel (quick, unpackaged):**

```jsonc
// .mcp.json
{ "mcpServers": { "tgctl-claude-channel": { "command": "/path/to/bin/tgctl-claude-channel" } } }
```

```sh
claude --dangerously-load-development-channels server:tgctl-claude-channel
```

> The dev flag asks for confirmation on **every** launch, so it can't auto-restart cleanly
> under systemd — use the **plugin** path for an always-on deployment.

## Security model

Telegram delivers webhooks to a **public** HTTPS endpoint, so defense is layered:

1. **Secret token** — Telegram echoes `TGCTL_CHANNEL_SECRET` in a header on every POST; `tgctl` rejects anything without it. This is what makes a public URL safe.
2. **Cloudflare Tunnel** — front the local port so it is never directly exposed.
3. **Telegram IP ranges** — optionally allowlist `149.154.160.0/20`, `91.108.4.0/22` at the proxy.
4. **Sender allowlist** — only configured `user_id`s can drive the agent; everything else is dropped before any notification is emitted.

A Telegram message ultimately drives an agent, so the sender allowlist + secret token are non-negotiable.

## Why hand-rolled (not an MCP SDK)

The `claude/channel` contract needs a **custom experimental capability** plus a **custom server notification** (`notifications/claude/channel`) — which the Go MCP SDKs don't expose cleanly. The MCP stdio transport is just newline-delimited JSON-RPC 2.0, so owning stdin/stdout directly is small and makes both trivial.

MIT.
