#!/bin/bash
# Recovers issues that were incorrectly tombstoned by bd sync
# See az-zby for bug details
#
# Strategy:
# 1. Try current JSONL first (fast path)
# 2. If not in JSONL, search git history for the removal commit
# 3. Extract from parent commit where issue still existed
# 4. Skip obvious test issues

set -euo pipefail
cd "$(dirname "$0")/.." || exit 1

echo "=== Recovering tombstoned issues ==="

tombstoned=$(sqlite3 .beads/beads.db "SELECT id FROM issues WHERE status='tombstone';")
recovered=0
skipped=0

recover_issue() {
  local id="$1"
  local jsonl_line="$2"

  local status
  local closed_at
  local title

  status=$(echo "$jsonl_line" | jq -r '.status')
  closed_at=$(echo "$jsonl_line" | jq -r '.closed_at // empty')
  title=$(echo "$jsonl_line" | jq -r '.title // empty')

  # Skip test issues and deleted markers
  if [[ "$title" =~ ^test|^\[Deleted\]|^\(deleted\) ]]; then
    echo "  Skipping test issue: $id ($title)"
    return 1
  fi

  # Skip if already tombstone in source
  if [ "$status" = "tombstone" ]; then
    echo "  Skipping (already tombstone in source): $id"
    return 1
  fi

  # Apply recovery
  if [ "$status" = "closed" ]; then
    if [ -n "$closed_at" ]; then
      sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at='$closed_at' WHERE id='$id';"
    else
      local now
      now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
      sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at='$now' WHERE id='$id';"
    fi
  else
    sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at=NULL WHERE id='$id';"
  fi

  # Also add back to JSONL if not present
  if ! jq -e "select(.id == \"$id\")" .beads/issues.jsonl >/dev/null 2>&1; then
    echo "$jsonl_line" >> .beads/issues.jsonl
  fi

  echo "  Recovered: $id -> $status"
  return 0
}

for id in $tombstoned; do
  echo "Checking $id..."

  # Strategy 1: Try current JSONL
  jsonl_line=$(jq -c "select(.id == \"$id\")" .beads/issues.jsonl 2>/dev/null | head -1)

  if [ -n "$jsonl_line" ]; then
    if recover_issue "$id" "$jsonl_line"; then
      ((recovered++)) || true
    else
      ((skipped++)) || true
    fi
    continue
  fi

  # Strategy 2: Search git history
  # Find the commit where this issue was removed (count changed)
  removal_commit=$(git log --all --oneline -1 -S "\"$id\"" -- .beads/issues.jsonl 2>/dev/null | cut -d' ' -f1)

  if [ -z "$removal_commit" ]; then
    echo "  Not found in git history: $id"
    ((skipped++)) || true
    continue
  fi

  # Get from parent commit (before removal)
  jsonl_line=$(git show "${removal_commit}^:.beads/issues.jsonl" 2>/dev/null | jq -c "select(.id == \"$id\")" | head -1)

  if [ -z "$jsonl_line" ]; then
    # Maybe it was added and removed in same commit, try the commit itself
    jsonl_line=$(git show "${removal_commit}:.beads/issues.jsonl" 2>/dev/null | jq -c "select(.id == \"$id\")" | head -1)
  fi

  if [ -n "$jsonl_line" ]; then
    if recover_issue "$id" "$jsonl_line"; then
      ((recovered++)) || true
    else
      ((skipped++)) || true
    fi
  else
    echo "  Could not extract from git: $id"
    ((skipped++)) || true
  fi
done

echo ""
echo "=== Recovery complete ==="
echo "Recovered: $recovered issues"
echo "Skipped:   $skipped issues"
