# List available commands
default:
    @just --list

# ts-opentui build (source)
build-ts:
    @echo "Building ts-opentui"
    cd ./ts-opentui && bun run build

# ts-opentui build and run binary
build-run-ts: build-sfe-ts
    @echo "Running ts-opentui binary"
    cd ./ts-opentui && ./bin/az

# ts-opentui build and run binary with --verbose
build-run-ts-verbose: build-sfe-ts
    @echo "Running ts-opentui binary with --verbose"
    cd ./ts-opentui && ./bin/az --verbose

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
    cd ./ts-opentui && ./bin/az

# Backward-compatible alias
ts-build-link-run: run-sfe-ts

# OpenCode plugin installer helper
install-opencode-az-plugin *repos:
    ./bin/install-opencode-az-plugin {{ repos }}

# Release helper: just release-ts-opentui [minor|patch|major]
release-ts-opentui bump='patch' *args:
    @set -eu; \
      target="{{ bump }}"; \
      target="${target#bump=}"; \
      ./ts-opentui/scripts/release.sh "$target" {{ args }}

# Release helper with Homebrew tap update:
# default tap dir: /Users/riordan/prog/homebrew-azedarach (override with AZ_HOMEBREW_TAP_DIR)
# just release-ts-opentui-homebrew patch
release-ts-opentui-homebrew bump='patch' *args:
    @set -eu; \
      target="{{ bump }}"; \
      target="${target#bump=}"; \
      ./ts-opentui/scripts/release-with-homebrew.sh "$target" --tap-dir "${AZ_HOMEBREW_TAP_DIR:-/Users/riordan/prog/homebrew-azedarach}" -- {{ args }}

# ts-opentui package-scoped validation for daemon lifecycle boundary migration
check-ts-daemon-contract:
    @echo "Type-checking @azedarach/daemon-control"
    cd ./ts-opentui && bun x tsc --noEmit -p packages/daemon-control/tsconfig.json

check-ts-daemon-runtime:
    @echo "Type-checking @azedarach/daemon"
    cd ./ts-opentui && bun run --filter @azedarach/daemon type-check

check-ts-boundaries:
    @echo "Running ts-opentui package boundary checks"
    cd ./ts-opentui && bun run check:boundaries

check-ts-hard-boundary-wave: check-ts-daemon-contract check-ts-daemon-runtime check-ts-boundaries
    @echo "Hard-boundary package checks complete"
