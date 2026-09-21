# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- **Second layer of bot-token redaction (jjuanrivvera/tgctl#21).** The Bot API carries its
  credential in the URL path, so a transport-level failure (a dropped connection, a read
  timeout) made Go print the token inside `*url.Error` — and tgctl's stderr, which this
  channel captures verbatim, became the text of an MCP tool result stored in the agent's
  transcript. tgctl now redacts at the source; the channel redacts again at its own edge,
  so an old binary on the host or a future code path cannot leak it either:
  - every JSON-RPC frame is scrubbed as it is serialized in `out.send` — the single choke
    point every tool result, protocol error and channel notification passes through;
  - the process log goes through a redacting writer, so a token in an error we merely log
    never lands on the host;
  - the non-secret bot id is kept (`123456789:<redacted>`), so a redacted error still says
    which bot failed and why.

## [0.8.0] - 2026-07-28

### Added
- **Busy notice when the session is parked (#5).** When the session is stuck on an
  interactive prompt (an `AskUserQuestion` / modal menu) it stops processing turns, so
  Telegram messages queue silently and the bot looks dead. The channel now sends **one**
  debounced "session busy / waiting — your message is queued" notice per unanswered turn,
  cancelled the moment a reply lands and re-armed on the next turn. Off by default; enabled
  by `TGCTL_CHANNEL_BUSY_NOTICE_DELAY` (a Go duration like `45s`, or bare seconds), with the
  wording overridable via `TGCTL_CHANNEL_BUSY_NOTICE_TEXT`. The channel process cannot see
  the session's TUI state, so this fires on any turn unanswered past the delay (a parked
  prompt *or* a genuinely long turn); a full prompt-to-Telegram bridge would need a
  session-side hook the channel does not have. Added both new vars to the plugin's `.mcp.json`
  env allowlist so they reach the process in plugin mode.

### Fixed
- Two channel instances on one host (different bots/tokens) no longer clobber each
  other's `getUpdates` cursor into an infinite re-delivery loop. The default poll-offset
  file is now derived per bot — `poll-offset-<bot_id>`, where `<bot_id>` is the numeric
  prefix of the token (`<botid>:<secret>`, resolved with no network call) — so multiple
  instances are safe by default. An explicit `TGCTL_CHANNEL_OFFSET_FILE` still takes
  precedence, and an existing single-instance `poll-offset` cursor is migrated into the
  new per-bot file on first run so no backlog is re-delivered. As defense in depth the
  poller now takes an exclusive `flock` on its cursor file and refuses to start (with a
  clear error) if another live instance already holds it. In tgctl-keyring mode (no
  `TGCTL_TOKEN`) the bot id is unknown, so the legacy shared name is kept. (#3)

## [0.6.0] — 2026-07-05

### Added
- Local event-injection listener (`/inject`): authenticated HTTP endpoint that turns local
  system events (cron, daemons, home automation) into channel turns with `meta.source:
  "system"` — event-driven notifications with no polling loop in the session. Off by
  default; enabled via `TGCTL_CHANNEL_INJECT_PORT` + `TGCTL_CHANNEL_INJECT_SECRET`
  (fail-closed without a secret). Context keys are namespaced (`ctx_*`) so injected events
  can never impersonate a Telegram sender. (#1)

### Fixed
- The plugin's `.mcp.json` `env` allowlist was missing `TGCTL_CHANNEL_INJECT_PORT`,
  `TGCTL_CHANNEL_INJECT_SECRET`, `TGCTL_CHANNEL_INJECT_BIND`, `TGCTL_CHANNEL_COMMAND_HANDLER`
  and `TGCTL_CHANNEL_TMUX_TARGET` — when loaded as a Claude Code plugin, only env vars
  listed there reach the channel process, so `/inject`, command handlers, and the
  tmux-target example never received their config in that mode even when set correctly
  in the shell.
- `runInject`'s `http.Server` only set `ReadHeaderTimeout`; added `ReadTimeout`,
  `WriteTimeout` and `IdleTimeout` so slow or idle clients can't hold the listener open.

## [0.5.0] — 2026-07-03

### Added
- **Interactive command handlers via callback routing.** A handler can now own inline-button taps: a callback whose `callback_data` is namespaced `hnd:` routes to the handler's `callback` subcommand (operator-only) instead of the model, so a handler can present native Telegram keyboards and act on the choice. The bundled use is a native `/model` and `/effort` picker — tap a button and the arg form runs directly, no TUI navigation.

## [0.4.0] — 2026-07-03

### Added
- **Command handlers** (`TGCTL_CHANNEL_COMMAND_HANDLER`): route recognized bot commands to a local executable instead of relaying them as a turn. The handler declares its commands (`list`) and performs them (`run`); the channel registers them in Telegram's command menu and relays their output. Operator-only. A generic extension point — the flagship use is driving the host Claude Code REPL to run **built-in slash commands** (`/model`, `/clear`, `/compact`, `/doctor`, …), which channel input otherwise cannot reach.

## [0.3.0] — 2026-07-02

Feature parity with the official Telegram channel, keeping the richer outbound toolbox and the `tgctl`-as-transport design.

### Added
- **Permission relay** (`claude/channel/permission`): tool-approval prompts are relayed to Telegram as **Allow / Deny** buttons (or a `yes/no <code>` text reply), so a session keeps its permission sandbox — `--dangerously-skip-permissions` is now optional.
- **Inbound attachments**: photos download to the inbox with `image_path`; documents, voice, audio, video, video notes and stickers carry attachment metadata; new **`download_attachment`** tool.
- **Access control**: `pairing` (6-char codes), `allowlist` and `disabled` policies; per-group policies with **mention detection**; `access.json` with atomic writes, env seeding, and corrupt-file recovery.
- **`reply`**: file attachments (images as photos, others as documents) and automatic chunking past Telegram's 4096-char limit.
- Bot commands `/start`, `/help`, `/status`.
- Richer inbound metadata (`ts`, `user`).

### Changed
- Outbound tools are gated on the chat allowlist — a prompt-injected `chat_id` can't reach a stranger.
- Robust process lifecycle: PID file with stale-poller (409) handling, clean shutdown so no zombie holds the bot token, an orphan watchdog, and polling backoff.

### Quality
- 83% test coverage (with `-race`), enforced by a coverage floor in CI and a pre-commit hook. golangci-lint, gofmt and vet wired into the Makefile and CI.

## [0.2.0] — 2026-07-02

### Changed
- **Inbound switched from webhook to long-poll** (`tgctl updates get`): no public endpoint, no tunnel, immune to edge WAFs blocking webhook POSTs. The getUpdates cursor is persisted.

### Added
- Full outbound toolbox: `reply` (with inline buttons), `react`, `edit`, `poll`, `photo`, `document`, `dice`, `pin`, `unpin`, `answer_callback`.
- Inbound `callback_query` handling, so button taps come back as channel turns.
- A "seen" reaction on receipt and a live "typing…" indicator while the assistant works.

## [0.1.0] — 2026-06-29

Initial release: a Claude Code channel bridging a Telegram bot to a session over the `tgctl` CLI, with a sender allowlist, `reply`/`react`/`edit` tools, an MCP + agent surface, and a VPS deploy kit.

[0.5.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.5.0
[0.4.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.4.0
[0.3.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.3.0
[0.2.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.2.0
[0.1.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.1.0
