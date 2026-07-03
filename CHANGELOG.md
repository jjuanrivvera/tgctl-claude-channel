# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.4.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.4.0
[0.3.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.3.0
[0.2.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.2.0
[0.1.0]: https://github.com/jjuanrivvera/tgctl-claude-channel/releases/tag/v0.1.0
