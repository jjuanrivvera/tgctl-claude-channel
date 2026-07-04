# Example: a live "what is Claude doing" feed

Keep **one self-editing Telegram message** that shows what the session is doing as it works —
a rolling trail of the last few tool calls — with a **[detalle]** button that expands the
content (diffs, command output, grep hits, subagent summaries) on demand. No message-per-step,
so the chat never floods.

This is a companion to [`claude-slash`](../claude-slash): it uses the same `tgctl` transport and
the channel's `hnd:` [callback routing](../../README.md#command-handlers) for its expand button,
but the activity itself comes from **Claude Code hooks**, not the channel.

## Two tiers

- **Live (Tier 1):** a compact rolling trail (last 6 actions) in one edited message, throttled
  to ~1 edit / 3s (Telegram rate-limits edits):
  ```
  🤖 Trabajando… · 7 tools · 34s
  📖 auth.go
  🔍 "login" → …
  ✏️ handler.go (+12/−3)
  ▶️ go test ./...                     [📋 detalle]
  ```
- **Detail (Tier 2, tap 📋):** the content of the recent actions that have any — an `Edit` diff,
  a `Bash` `$ cmd` + output, `Grep` hits, a `Task` summary — each in a `<pre>` block, truncated.
- **On finish:** collapses to `✓ Listo · N tools · Ns` (button dropped).

Each tool has a `render()` entry producing `(compact_line, detail_block)`; adding a tool is one
branch. Reads/globs show a compact line but no detail (signal over noise).

## How it hooks in

`PostToolUse` fires on every tool call with `{tool_name, tool_input, tool_response}` on stdin —
structured access to each action, no screen-scraping. `Stop` finalizes the message. A hook must
never disturb the turn, so the script **prints nothing and always exits 0**, even on error.

## Setup

1. Copy `activity` next to `claude-slash` (e.g. `~/bin/activity`, `chmod +x`). `claude-slash`
   delegates `hnd:activity:*` button taps to it, so keep them in the same directory.
2. Tell it where to post (your chat id — for a DM it's your user id):
   ```sh
   export TG_ACTIVITY_CHAT=123456789
   ```
3. Register the hooks in the session's settings (`~/.claude/settings.json` on the box running it):
   ```json
   {
     "hooks": {
       "PostToolUse": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "~/bin/activity tool" }] }],
       "Stop":        [{ "hooks": [{ "type": "command", "command": "~/bin/activity stop" }] }]
     }
   }
   ```
4. Restart the session. Send a message that makes Claude use tools — the feed appears and updates
   in place.

## Requirements

- **`python3`**, **`tgctl`** (reached via the inherited `TGCTL_TOKEN` / `TGCTL_BIN`).
- The channel running with the command-handler feature wired (for the [📋] button to route back).

## Caveats

- Telegram rate-limits edits — the ~3s throttle is deliberate; the last actions land at `Stop`.
- Turns with no tool calls (pure text) show nothing here — the channel's typing indicator covers that.
- Detail blocks are truncated (`DETAIL_CAP`); it's a glance, not a transcript.
- Single-chat by design (posts to `TG_ACTIVITY_CHAT`). Multi-chat routing would need the channel
  to expose the active turn's chat id.
