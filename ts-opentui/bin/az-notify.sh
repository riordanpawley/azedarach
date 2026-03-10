#!/bin/bash
#
# az-notify.sh - Lightweight hook notification for Azedarach
#
# Usage: az-notify.sh <event> <beadId> [sessionName]
#
# Events and their status mappings:
#   user_prompt, pretooluse → busy
#   idle_prompt, permission_request, stop → waiting
#   session_end → idle
#
# This script is designed to be FAST (<10ms) by directly calling tmux
# instead of going through the full bun/TypeScript CLI.
#

# Debug log file (comment out LOG_FILE to disable)
LOG_FILE="/tmp/az-notify-debug.log"
WAITING_ALERT_OPTION="@az_waiting_alerted"
WAITING_WINDOW_BELL_STYLE="fg=colour226,bg=colour237,bold"
WAITING_WINDOW_ACTIVITY_STYLE="fg=colour220,bg=colour237,bold"

log() {
	if [ -n "$LOG_FILE" ]; then
		echo "[$(date '+%Y-%m-%d %H:%M:%S.%3N')] $*" >>"$LOG_FILE"
	fi
}

# Get arguments
EVENT="$1"
BEAD_ID="$2"
SESSION_NAME_ARG="$3"

if [ -z "$EVENT" ] || [ -z "$BEAD_ID" ]; then
	log "ERROR: Missing arguments. Usage: az-notify.sh <event> <beadId>"
	exit 1
fi

log "=== HOOK FIRED: event=$EVENT beadId=$BEAD_ID ==="

# Map event to status
case "$EVENT" in
user_prompt | pretooluse)
	STATUS="busy"
	;;
idle_prompt | permission_request | stop)
	STATUS="waiting"
	;;
session_end)
	STATUS="idle"
	;;
*)
	log "ERROR: Unknown event type: $EVENT"
	exit 1
	;;
esac

# Prefer canonical tmux session name when provided by hook config generation.
# Fallback to bead ID for backwards compatibility with older hook configs.
SESSION_NAME="${SESSION_NAME_ARG:-$BEAD_ID}"
log "Setting @az_status=$STATUS on session $SESSION_NAME"

# Set tmux session option
# Use 2>/dev/null to suppress errors if session doesn't exist yet
tmux set-option -t "$SESSION_NAME" @az_status "$STATUS" 2>/dev/null
EXIT_CODE=$?

if [ $EXIT_CODE -eq 0 ]; then
	log "SUCCESS: @az_status=$STATUS set on $SESSION_NAME"
else
	log "WARN: Could not set status (session may not exist yet). Exit code: $EXIT_CODE"
fi

# Keep alert styling session-local so waiting states are easy to notice in tmux
# without forcing a global theme change.
tmux set-option -t "$SESSION_NAME" monitor-bell on 2>/dev/null || true
tmux set-option -t "$SESSION_NAME" monitor-activity on 2>/dev/null || true
tmux set-option -t "$SESSION_NAME" bell-action any 2>/dev/null || true
tmux set-option -t "$SESSION_NAME" activity-action any 2>/dev/null || true
tmux set-option -t "$SESSION_NAME" window-status-bell-style "$WAITING_WINDOW_BELL_STYLE" 2>/dev/null || true
tmux set-option -t "$SESSION_NAME" window-status-activity-style "$WAITING_WINDOW_ACTIVITY_STYLE" 2>/dev/null || true

if [ "$STATUS" = "waiting" ]; then
	ALERTED="$(tmux show-option -t "$SESSION_NAME" -v "$WAITING_ALERT_OPTION" 2>/dev/null | tr -d '[:space:]')"
	if [ "$ALERTED" != "1" ]; then
		PANE_TTY="$(tmux display-message -p -t "$SESSION_NAME" '#{pane_tty}' 2>/dev/null | tr -d '\r\n')"
		if [ -n "$PANE_TTY" ] && [ -w "$PANE_TTY" ]; then
			if printf '\a' >"$PANE_TTY" 2>/dev/null; then
				ALERTED="1"
				log "SUCCESS: Rung tmux bell via pane tty $PANE_TTY"
			else
				ALERTED="0"
				log "WARN: Failed to ring tmux bell for $SESSION_NAME"
			fi
		else
			ALERTED="0"
			log "WARN: pane tty unavailable for $SESSION_NAME"
		fi
	fi
	tmux set-option -t "$SESSION_NAME" "$WAITING_ALERT_OPTION" "$ALERTED" 2>/dev/null || true
else
	tmux set-option -t "$SESSION_NAME" "$WAITING_ALERT_OPTION" "0" 2>/dev/null || true
fi

# Output valid JSON for hook systems that parse command output
echo "{}"

exit 0
