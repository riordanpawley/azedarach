# List available commands
default:
    @just --list

# ts-opentui build (source)
build-ts:
    @echo "Building ts-opentui"
    cd ./ts-opentui && bun run build

# go-bubbletea build (binary)
build-go:
    @echo "Building go-bubbletea binaries"
    cd ./go-bubbletea && just build

# ts-opentui build and run from source
build-run-ts: build-ts
    @echo "Running ts-opentui from source"
    cd ./ts-opentui && bun run bin/az.ts

# ts-opentui build and run from source with --verbose
build-run-ts-verbose: build-ts
    @echo "Running ts-opentui from source with --verbose"
    cd ./ts-opentui && bun run bin/az.ts --verbose

# go-bubbletea build and run from local binary
build-run-go: build-go
    @echo "Running go-bubbletea from local binary"
    ./go-bubbletea/bin/az --help

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

# Link go-bubbletea az into PATH as az-go (respects AZ_GO_LINK_DIR when set)
link-go:
    @echo "Linking go-bubbletea az into PATH as az-go"
    @set -eu; \
      if [ -n "${AZ_GO_LINK_DIR:-}" ]; then \
        bin_dir="${AZ_GO_LINK_DIR}"; \
        mkdir -p "$bin_dir"; \
      elif command -v az-go >/dev/null 2>&1 && [ -w "$(dirname "$(command -v az-go)")" ]; then \
        bin_dir="$(dirname "$(command -v az-go)")"; \
      elif command -v brew >/dev/null 2>&1 && [ -w "$(brew --prefix)/bin" ]; then \
        bin_dir="$(brew --prefix)/bin"; \
      else \
        bin_dir="$HOME/.local/bin"; \
        mkdir -p "$bin_dir"; \
      fi; \
      ln -sf "$(pwd)/go-bubbletea/bin/az" "$bin_dir/az-go"; \
      echo "az-go -> $bin_dir/az-go"

install-go: build-go link-go
    @echo "Installed go-bubbletea binary. Try: az-go --help"

run-go: install-go
    @echo "Running freshly built go-bubbletea binary via az-go"
    az-go --help

go-build-link-run: run-go

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
