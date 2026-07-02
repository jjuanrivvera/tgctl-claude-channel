#!/usr/bin/env bash
# channel-up.sh — bring up the tgctl-claude-channel session (long-poll mode), headless.
# Resets stale state, launches claude detached in tmux with the dev-channel flag,
# auto-answers the startup prompts, and verifies. The channel server long-polls
# Telegram (tgctl updates get) — no webhook, no tunnel, no Cloudflare, no inbound port.
#
# Config: override these via env if your layout differs.
set -uo pipefail
SESSION="${CHANNEL_TMUX:-claude-tg}"
ENV="${CHANNEL_ENV:-$HOME/.config/tgctl-claude/channel.env}"
WORKDIR="${CHANNEL_WORKDIR:-$HOME/Assistant}"
strip(){ sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g; s/\x1b[()][B0]//g'; }

echo "== 1. reset: kill old channel/poller/session =="
tmux kill-session -t "$SESSION" 2>/dev/null || true
pkill -f "bin/tgctl-claude-channel" 2>/dev/null || true
pkill -f "tgctl updates get"        2>/dev/null || true
pkill -f "tgctl webhook listen"     2>/dev/null || true   # legacy webhook mode
sleep 2
echo "   reset done"

echo "== 2. launch claude in detached tmux (polling channel, skip-permissions) =="
tmux new-session -d -s "$SESSION" -c "$WORKDIR" -x 220 -y 50
tmux send-keys -t "$SESSION" "set -a; source '$ENV'; set +a; claude --dangerously-load-development-channels server:tgctl-claude-channel --dangerously-skip-permissions" Enter

echo "== 3. auto-answer prompts (dev-channels / MCP-enable / trust) =="
active=0
for i in $(seq 1 30); do
  sleep 3
  pane=$(tmux capture-pane -p -S -50 -t "$SESSION" 2>/dev/null | strip)
  echo "$pane" | grep -qiE 'inject directly|Channels \(experimental\)' && { active=1; echo "   >>> CHANNEL ACTIVE"; break; }
  echo "$pane" | grep -qiE 'trust the files|do you trust|trust this folder' && { echo "   [$i] trust -> Enter"; tmux send-keys -t "$SESSION" Enter; continue; }
  echo "$pane" | grep -qiE 'new MCP servers found'                          && { echo "   [$i] MCP-enable -> Enter"; tmux send-keys -t "$SESSION" Enter; continue; }
  echo "$pane" | grep -qiE 'development channels|local development'          && { echo "   [$i] dev-channels -> Enter (option 1)"; tmux send-keys -t "$SESSION" Enter; continue; }
done

echo "== 4. verify =="
sleep 4
pgrep -f "bin/tgctl-claude-channel" >/dev/null && echo "   channel server: running" || echo "   channel server: NOT running"
pgrep -f "tgctl updates get"        >/dev/null && echo "   poller: alive"           || echo "   poller: (between polls)"
if [ "$active" = 1 ]; then
  echo ">>> CHANNEL UP. Message the bot — long-poll, no webhook/Cloudflare in the path."
else
  echo ">>> channel not confirmed active. Last pane:"
  tmux capture-pane -p -S -30 -t "$SESSION" 2>/dev/null | strip | grep -v '^[[:space:]]*$' | tail -20
fi
