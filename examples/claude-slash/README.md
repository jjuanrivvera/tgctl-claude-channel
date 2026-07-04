# Example: run Claude Code built-in slash commands from Telegram

A reference [command handler](../../README.md#command-handlers) that runs Claude Code's
**built-in slash commands** — `/model`, `/effort`, `/clear`, `/compact`, `/cost`, `/doctor`,
`/mcp`, `/goal` — from Telegram, with native inline-keyboard pickers for `/model` and `/effort`.

This is the capability the official Telegram integration can't offer: it only delivers messages
to the model, never to the client harness. Routing through `tgctl` + this handler gives you
control of the harness itself from your phone.

## Why a handler is required

Built-in slash commands are **client-side harness operations** — the model can't trigger them,
and a channel's inbound text reaches the model wrapped in `<channel>…</channel>` tags, never
through the slash parser. So sending `/model` as a message does nothing useful. The only way to
run a built-in is to type it into the real REPL.

This handler does exactly that: the Claude Code session runs inside **tmux**, so the handler
drives it with `tmux send-keys` and mirrors the result back with `tmux capture-pane`. That
REPL/tmux coupling is deployment-specific — which is why it lives here as an example and not in
the channel core. The channel stays generic; this is one thing you plug into it.

## How it maps onto the generic feature

| Generic plugin primitive | What this handler does |
|---|---|
| `list` → command manifest | declares `model, effort, status, cost, compact, clear, doctor, mcp, goal` |
| `run <cmd> <args>` | types the slash command into the REPL, captures the settled overlay, sends it back as a `<pre>` block |
| `hnd:` callback routing | `/model` and `/effort` (no args) send inline keyboards; a tap runs the arg form (`/model opus`, `/effort high`) and edits the message to the result |

Nothing about tmux, Claude, models or effort is in the channel — only this handler knows about them.

## Data flow

```
Telegram "/model"
  → channel: recognized command, operator-gated → `claude-slash run model ""`
  → handler: sends inline keyboard [Opus][Sonnet][Haiku][Fable]  (callback_data hnd:model:…)
Telegram tap "Sonnet 5"
  → channel: callback data starts with hnd: → `claude-slash callback hnd:model:sonnet`
  → handler: tmux send-keys "/model sonnet" Enter  → answers the tap, edits the message to
             "✅ Model → Sonnet 5"
```

## Setup

1. Copy `claude-slash` to the machine running the session (e.g. `~/bin/claude-slash`) and
   `chmod +x` it.
2. Run the Claude Code session in a **named tmux session** (e.g. `tmux new -s claude-tg`, then
   launch `claude` inside it).
3. Point the channel at the handler and tell it the tmux target:
   ```sh
   export TGCTL_CHANNEL_COMMAND_HANDLER=$HOME/bin/claude-slash
   export TGCTL_CHANNEL_TMUX_TARGET=claude-tg    # tmux target of the REPL pane
   ```
4. (Re)start the channel. In Telegram, type `/` — the commands autocomplete; `/model` and
   `/effort` pop native keyboards.

## Requirements

- **`tmux`** — the session must run in it (the handler drives the pane).
- **`python3`** — overlay cleaning and JSON assembly.
- **`tgctl`** — reached through the environment the channel passes down (`TGCTL_TOKEN`, `TGCTL_BIN`).

## Caveats

- Commands run for **allowlisted operators only** (the channel drops handler commands from anyone else).
- `/clear` and `/compact` are **destructive and run without confirmation** — they alter session state immediately.
- The mirror waits for the pane to settle (no fixed sleeps), so a command has a short delay before its output comes back.
- This is a reference example. Treat it as a starting point — adapt the command set, formatting and guards to your setup.
