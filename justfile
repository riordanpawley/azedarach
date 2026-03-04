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
