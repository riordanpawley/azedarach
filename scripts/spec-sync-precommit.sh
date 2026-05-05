#!/usr/bin/env sh
set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Configure one canonical spec export command in your environment.
# Example:
#   AZ_SPEC_SYNC_CMD='az spec export --target md' git commit
sync_cmd="${AZ_SPEC_SYNC_CMD:-}"

if [ -z "$sync_cmd" ]; then
  # Spec export is optional in environments without spec tooling configured.
  exit 0
fi

echo "pre-commit: exporting spec markdown..."
/bin/sh -c "$sync_cmd"

# Auto-stage exported markdown so commit content matches generated spec docs.
if [ -d docs/spec ]; then
  git add docs/spec
fi
