#!/usr/bin/env bash
set -euo pipefail

EVENT="${1:-unknown}"
CHANNEL="${2:-tc}"
GROUP="${3:-az-tc}"

PAYLOAD=""
if [ ! -t 0 ]; then
  PAYLOAD="$(cat || true)"
fi

SOURCE=""
CWD=""
if [ -n "$PAYLOAD" ] && command -v jq >/dev/null 2>&1; then
  SOURCE="$(printf '%s' "$PAYLOAD" | jq -r '.source // empty' 2>/dev/null || true)"
  CWD="$(printf '%s' "$PAYLOAD" | jq -r '.cwd // empty' 2>/dev/null || true)"
fi

TITLE="Azedarach Codex"
MESSAGE="$EVENT"

case "$EVENT" in
  user_prompt)
    if [ -n "$SOURCE" ]; then
      MESSAGE="Session started ($SOURCE)"
    else
      MESSAGE="Session started"
    fi
    ;;
  session_end)
    MESSAGE="Session stopped"
    ;;
  *)
    MESSAGE="$EVENT"
    ;;
esac

if [ -n "$CWD" ]; then
  MESSAGE="$MESSAGE - $(basename "$CWD")"
fi

if command -v osascript >/dev/null 2>&1; then
  /usr/bin/osascript -e "display notification \"$MESSAGE\" with title \"$TITLE\"" >/dev/null 2>&1 || true
  exit 0
fi

if command -v terminal-notifier >/dev/null 2>&1; then
  terminal-notifier \
    -title "$TITLE" \
    -message "$MESSAGE" \
    -group "$GROUP" >/dev/null 2>&1 || true
  exit 0
fi

# No notification backend available; fail open to avoid blocking Codex.
printf 'codex-notify-hook: %s (%s)\n' "$MESSAGE" "$CHANNEL" >&2
exit 0
