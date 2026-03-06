# List all available commands
default:
    @just --list

# Update all flake inputs
# Switch back one generation
build-ts:
    @echo "Building ts-opentui"
    cd ./ts-opentui && bun run build && cd ..

build-run-ts:
    @echo "Building & running ts-opentui"
    cd ./ts-opentui && bun run build && cd .. && bun run ./ts-opentui/bin/az.ts
build-sfe-ts:
    @echo "Building ts-opentui single-file executable"
    cd ./ts-opentui && bun build --compile ./bin/az.ts --outfile ./bin/az

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

ts-build-link-run: build-sfe-ts link-sfe-ts
    @echo "Running az"
    az

build-go:
    @echo "Building go-bubbletea"
    cd ./go-bubbletea && make build && cd ..

test-go:
    @echo "Testing go-bubbletea"
    cd ./go-bubbletea && make test && cd ..

run-go:
    @echo "Running go-bubbletea"
    cd ./go-bubbletea && make run && cd ..
