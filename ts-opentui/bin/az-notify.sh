#!/bin/bash
#
# az-notify.sh - Lightweight hook notification for Azedarach
#
# Usage: az-notify.sh <event> <beadId>
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

log() {
    if [ -n "$LOG_FILE" ]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S.%3N')] $*" >> "$LOG_FILE"
    fi
}

# Get arguments
EVENT="$1"
BEAD_ID="$2"

if [ -z "$EVENT" ] || [ -z "$BEAD_ID" ]; then
    log "ERROR: Missing arguments. Usage: az-notify.sh <event> <beadId>"
    exit 1
fi

log "=== HOOK FIRED: event=$EVENT beadId=$BEAD_ID ==="

# Map event to status
case "$EVENT" in
    user_prompt|pretooluse)
        STATUS="busy"
        ;;
    idle_prompt|permission_request|stop)
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

# Session name is just the bead ID (e.g., "az-05y")
# This matches the naming convention in paths.ts and session_manager.gleam
SESSION_NAME="${BEAD_ID}"
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

# Output valid JSON for hook systems that parse command output
echo "{}"

exit 0
