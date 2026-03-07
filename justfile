# List available commands
default:
    @just --list

# ts-opentui build (source)
build-ts:
    @echo "Building ts-opentui"
    cd ./ts-opentui && bun run build

# ts-opentui build and run from source
build-run-ts: build-ts
    @echo "Running ts-opentui from source"
    cd ./ts-opentui && bun run bin/az.ts

# ts-opentui build (single-file executable)
build-sfe-ts:
    @echo "Building ts-opentui single-file executable"
    cd ./ts-opentui && bun run build-sfe

# Link standalone az into PATH (respects AZ_LINK_DIR when set)
link-sfe-ts:
    @echo "Linking az into PATH"
    @set -eu; \
      if [ -n "${AZ_LINK_DIR:-}" ]; then \
        bin_dir="${AZ_LINK_DIR}"; \
        mkdir -p "$bin_dir"; \
      elif command -v az >/dev/null 2>&1 && [ -w "$(dirname "$(command -v az)")" ]; then \
        bin_dir="$(dirname "$(command -v az)")"; \
      elif command -v brew >/dev/null 2>&1 && [ -w "$(brew --prefix)/bin" ]; then \
        bin_dir="$(brew --prefix)/bin"; \
      else \
        bin_dir="$HOME/.local/bin"; \
        mkdir -p "$bin_dir"; \
      fi; \
      ln -sf "$(pwd)/ts-opentui/bin/az" "$bin_dir/az"; \
      echo "az -> $bin_dir/az"

install-sfe-ts: build-sfe-ts link-sfe-ts
    @echo "Installed az. Try: az --help"

run-sfe-ts: install-sfe-ts
    @echo "Running freshly built SFE"
    az

# Backward-compatible alias
ts-build-link-run: run-sfe-ts

# RuleSync helpers
rulesync-sync:
    rulesync generate -c rulesync.jsonc --silent

rulesync-check:
    rulesync generate -c rulesync.jsonc --check --silent

# OpenCode plugin installer helper
install-opencode-az-plugin *repos:
    ./bin/install-opencode-az-plugin {{ repos }}

# Release helper: just release-ts-opentui [minor|patch|major]
release-ts-opentui bump='patch' *args:
    ./ts-opentui/scripts/release.sh {{ bump }} {{ args }}
