#!/usr/bin/env sh
set -eu

mode="${1:-sync}"

case "$mode" in
  sync|check) ;;
  *)
    echo "Usage: $0 [sync|check]" >&2
    exit 2
    ;;
esac

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

if [ "$mode" = "sync" ]; then
  rulesync generate -c rulesync.jsonc --silent
else
  rulesync generate -c rulesync.jsonc --check --silent
fi

status=0

while IFS='|' read -r kind source target flags; do
  case "$kind" in
    ""|\#*) continue ;;
  esac

  case "$kind" in
    file)
      if [ ! -f "$source" ]; then
        echo "[rulesync] missing source file: $source" >&2
        status=1
        continue
      fi

      if [ "$mode" = "sync" ]; then
        mkdir -p "$(dirname "$target")"
        cp "$source" "$target"
        case "$flags" in
          *exec*) chmod +x "$target" ;;
        esac
      else
        if [ ! -f "$target" ] || ! cmp -s "$source" "$target"; then
          echo "[rulesync] out of sync file: $target (source: $source)" >&2
          status=1
        fi
      fi
      ;;
    dir)
      if [ ! -d "$source" ]; then
        echo "[rulesync] missing source dir: $source" >&2
        status=1
        continue
      fi

      if [ "$mode" = "sync" ]; then
        mkdir -p "$target"
        rsync -a --delete "$source"/ "$target"/
        case "$flags" in
          *exec*) find "$target" -type f -exec chmod +x {} + ;;
        esac
      else
        if [ ! -d "$target" ] || ! diff -rq "$source" "$target" >/dev/null 2>&1; then
          echo "[rulesync] out of sync dir: $target (source: $source)" >&2
          status=1
        fi
      fi
      ;;
    *)
      echo "[rulesync] unknown mapping kind: $kind" >&2
      status=1
      ;;
  esac
done < .rulesync/mappings.tsv

if [ "$status" -ne 0 ]; then
  exit 1
fi

if [ "$mode" = "sync" ]; then
  echo "[rulesync] ✓ sync complete"
else
  echo "[rulesync] ✓ check passed"
fi
