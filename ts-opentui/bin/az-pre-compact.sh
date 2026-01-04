#!/bin/bash
#
# az-pre-compact.sh - PreCompact hook for bead context preservation
#
# Usage: az-pre-compact.sh <beadId>
#
# This hook fires before Claude compacts its context window.
# It outputs a reminder that Claude will see, prompting it to update
# the bead with session progress before context is lost.
#
# Design decisions:
# - We output text instead of updating beads directly because Claude
#   has better context about what work was actually done
# - The reminder appears in Claude's context BEFORE compaction
# - Exit 0 allows compaction to proceed (exit 2 would block it)
#

# Debug log file (comment out LOG_FILE to disable)
LOG_FILE="/tmp/az-pre-compact-debug.log"

log() {
    if [ -n "$LOG_FILE" ]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S.%3N')] $*" >> "$LOG_FILE"
    fi
}

# Get bead ID from args
BEAD_ID="$1"

if [ -z "$BEAD_ID" ]; then
    log "ERROR: Missing bead ID. Usage: az-pre-compact.sh <beadId>"
    exit 1
fi

log "=== PRE-COMPACT HOOK FIRED: beadId=$BEAD_ID ==="

# Read and log stdin (JSON from Claude Code)
# We don't parse it currently but log it for debugging
if [ -t 0 ]; then
    log "No stdin (interactive mode)"
else
    STDIN_DATA=$(cat)
    log "Stdin: $STDIN_DATA"
fi

# Output reminder that Claude will see before compaction
# This text becomes part of Claude's context during the compact operation
cat << 'EOF'

<system-reminder>
PreCompact hook triggered - Context compaction is about to occur.

IMPORTANT: Before compaction, ensure your work is preserved in beads:

1. If you have in-progress work, update the bead with notes:
   ```bash
   bd update <bead-id> --notes="
   COMPLETED: [what was done]
   IN PROGRESS: [current state]
   NEXT: [concrete next step]
   KEY DECISIONS: [important context]
   "
   ```

2. If work is complete, close the bead:
   ```bash
   bd close <bead-id> --reason="[summary of what was accomplished]"
   ```

This ensures your progress survives compaction and future sessions can resume seamlessly.
</system-reminder>

EOF

log "SUCCESS: Pre-compact reminder output for bead $BEAD_ID"

# Exit 0 to allow compaction to proceed
exit 0
