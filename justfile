# List available commands
default:
    @just --list

build-install-run *ARGS:
    ./scripts/build-install-run.sh {{ARGS}}

# Backward-compatible alias. Prefer build-install-run in new automation/docs.
build-link-run *ARGS:
    ./scripts/build-install-run.sh {{ARGS}}

build:
    mkdir -p .tmp/az-test
    SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"; \
    LDFLAGS="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=$SHA"; \
    go build -ldflags "$LDFLAGS" -o .tmp/az-test/az ./cmd/az
    SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"; \
    LDFLAGS="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=$SHA"; \
    go build -ldflags "$LDFLAGS" -o .tmp/az-test/azd ./cmd/azd

run:
    just build
    AZ_BIN="$(pwd)/.tmp/az-test/az"; \
    DAEMON_BIN="$(pwd)/.tmp/az-test/azd"; \
    if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then \
        AZEDARACH_DAEMON_BIN="$DAEMON_BIN" AZEDARACH_DAEMON_SCOPE=worktree AZEDARACH_DAEMON_SCOPE_SOURCE=just-run "$AZ_BIN" daemon restart; \
        AZEDARACH_DAEMON_SCOPE=worktree AZEDARACH_DAEMON_SCOPE_SOURCE=just-run "$AZ_BIN"; \
    else \
        AZEDARACH_DAEMON_BIN="$DAEMON_BIN" "$AZ_BIN" daemon restart; \
        "$AZ_BIN"; \
    fi

bench-git-runtime *ARGS:
    go run ./cmd/bench-git-runtime {{ARGS}}

bench-parallel-cli-latency:
    ./scripts/bench-parallel-cli-latency.sh

test:
    just test-timing cold

# Canonical machine-readable timing profiles. Additional flags can be passed
# after the recipe name, for example: just test-timing focused --package ./internal/cli --run TestName
test-timing PROFILE="focused" *ARGS:
    go run ./cmd/test-timing --profile {{PROFILE}} {{ARGS}}

test-fast *ARGS:
    just test-timing focused {{ARGS}}

test-integration:
    just test-timing integration

test-migration-clone:
    just test-timing migration-clone

test-race:
    just test-timing race

test-boundary:
    just test-timing boundary

test-build-contract:
    ./scripts/test-build-artifact-isolation.sh

merge-gate:
    just build
    just test
    just test-build-contract
    just check-boundaries

# Aggregate daemon race validation has a larger budget than focused race tests.
# The timeout remains inside `go test` so genuine hangs emit goroutine stacks.
test-race-daemon:
    go run ./cmd/go-cache run --kind race -- ./scripts/test-daemon-race-sharded.sh

test-coverage:
    go run ./cmd/go-cache run --kind coverage -- go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

go-cache-inventory:
    go run ./cmd/go-cache inventory

go-cache-maintain:
    go run ./cmd/go-cache maintain

go-cache-clean-owner ISSUE:
    go run ./cmd/go-cache cleanup-owner --issue {{ISSUE}} --confirm

go-cache-clean-legacy *ARGS:
    go run ./cmd/go-cache cleanup-legacy {{ARGS}}

type-check:
    go build ./...

clean:
    rm -rf .tmp/az-test/ .tmp/cli-smoke/ coverage.out coverage.html

install:
    @echo "Refusing unpaired install: run 'just build-install-run --no-run' from the primary worktree" >&2
    @exit 1

lint:
    golangci-lint run ./...

check-boundaries:
    if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run --config .golangci-boundary.yml ./internal/...; else echo "WARN: golangci-lint not installed; skipping depguard boundary lint gate" >&2; fi
    ./scripts/check-boundaries.sh
    ./scripts/afv-drift-sentinel.sh
    env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE just test-boundary

boundary-check:
    @just check-boundaries

release-homebrew *ARGS:
    ./scripts/release-homebrew.sh {{ARGS}}

git-config-lock:
    git rev-parse --is-inside-work-tree >/dev/null
    if [ "$(git config --local --get core.bare || true)" != "false" ]; then git config --local core.bare false; fi
    chflags uchg .git/config
    @just git-config-status

git-config-unlock:
    git rev-parse --is-inside-work-tree >/dev/null
    chflags nouchg .git/config
    @just git-config-status

git-config-status:
    git config --show-origin --get core.bare || true
    if ls -lO .git/config >/dev/null 2>&1; then ls -lO .git/config; elif command -v lsattr >/dev/null 2>&1; then lsattr .git/config; else ls -l .git/config; fi
