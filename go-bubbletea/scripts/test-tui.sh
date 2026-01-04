#!/bin/bash
set -euo pipefail

TUI_BIN="./go-bubbletea/bin/az"
TMUX_WINDOW="az-test"
CAPTURE_DIR="./test-captures"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p "$CAPTURE_DIR"

echo "🧪 Starting TUI test run: $TIMESTAMP"

capture_tui() {
	local filename="$1"
	tmux capture-pane -t "$TMUX_WINDOW" -p >"$CAPTURE_DIR/$filename"
	echo "  📸 Captured: $filename"
}

echo "🚀 Launching TUI in tmux window..."
tmux new-window -n "$TMUX_WINDOW" -c "$(pwd)/go-bubbletea"
sleep 1
tmux send-keys -t "$TMUX_WINDOW" "$TUI_BIN" C-m
sleep 2
capture_tui "01-initial.png"

echo "➡️  Testing basic navigation..."
tmux send-keys -t "$TMUX_WINDOW" j C-m
sleep 1
capture_tui "02-after-down.png"

tmux send-keys -t "$TMUX_WINDOW" k C-m
sleep 1
capture_tui "03-after-up.png"

tmux send-keys -t "$TMUX_WINDOW" l C-m
sleep 1
capture_tui "04-after-left.png"

tmux send-keys -t "$TMUX_WINDOW" h C-m
sleep 1
capture_tui "05-after-right.png"

echo "📜 Testing column scrolling..."
for i in {1..5}; do
	tmux send-keys -t "$TMUX_WINDOW" j C-m
	sleep 0.3
done
capture_tui "06-after-scroll.png"

echo "❓ Testing help overlay..."
tmux send-keys -t "$TMUX_WINDOW" '?' C-m
sleep 2
capture_tui "07-help-overlay.png"

tmux send-keys -t "$TMUX_WINDOW" 'esc' C-m
sleep 1
capture_tui "08-after-help-close.png"

echo "🔍 Testing search overlay..."
tmux send-keys -t "$TMUX_WINDOW" '/' C-m
sleep 2
tmux send-keys -t "$TMUX_WINDOW" 'test' C-m
sleep 1
capture_tui "09-search-overlay.png"

tmux send-keys -t "$TMUX_WINDOW" 'esc' C-m
sleep 1
capture_tui "10-after-search-close.png"

echo "🧹 Cleaning up tmux window..."
tmux kill-window -t "$TMUX_WINDOW" 2>/dev/null || true

echo "✅ Test run complete!"
echo "📁 Captures saved to: $CAPTURE_DIR"
echo ""
echo "Screenshots to review:"
ls -1 "$CAPTURE_DIR"*.png | while read file; do
	echo "  - $file"
done
