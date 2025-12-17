#!/bin/bash
# Recovers issues that were incorrectly tombstoned by bd sync
# See az-zby for bug details

cd "$(dirname "$0")/.." || exit 1

echo "=== Recovering tombstoned issues from JSONL ==="

tombstoned=$(sqlite3 .beads/beads.db "SELECT id FROM issues WHERE status='tombstone';")
recovered=0

for id in $tombstoned; do
  jsonl_line=$(jq -c "select(.id == \"$id\")" .beads/issues.jsonl 2>/dev/null)

  if [ -n "$jsonl_line" ]; then
    status=$(echo "$jsonl_line" | jq -r '.status')
    closed_at=$(echo "$jsonl_line" | jq -r '.closed_at // empty')

    if [ "$status" != "tombstone" ] && [ -n "$status" ]; then
      if [ "$status" = "closed" ] && [ -n "$closed_at" ]; then
        echo "Recovering $id: status -> $status"
        sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at='$closed_at' WHERE id='$id';"
        ((recovered++))
      elif [ "$status" != "closed" ]; then
        echo "Recovering $id: status -> $status"
        sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at=NULL WHERE id='$id';"
        ((recovered++))
      else
        now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
        echo "Recovering $id: status -> $status (generated closed_at)"
        sqlite3 .beads/beads.db "UPDATE issues SET status='$status', closed_at='$now' WHERE id='$id';"
        ((recovered++))
      fi
    fi
  fi
done

echo ""
echo "=== Recovered $recovered issues ==="
