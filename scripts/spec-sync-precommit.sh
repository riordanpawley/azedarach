#!/usr/bin/env sh
set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Configure one canonical sync command in your environment.
# Example:
#   AZ_SPEC_SYNC_CMD='spec sync --to-md docs/spec' git commit
sync_cmd="${AZ_SPEC_SYNC_CMD:-}"

if [ -z "$sync_cmd" ]; then
  # Spec sync is optional in environments without spec tooling configured.
  exit 0
fi

echo "pre-commit: syncing spec markdown..."
/bin/sh -c "$sync_cmd"

# Auto-stage synced markdown so commit content matches generated spec docs.
if [ -d docs/spec ]; then
  git add docs/spec
fi
