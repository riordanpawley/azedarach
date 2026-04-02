# List available commands
default:
    @just --list

build-link-run:
    ./scripts/build-link-run.sh

build:
    SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"; \
    LDFLAGS="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=$SHA"; \
    go build -ldflags "$LDFLAGS" -o bin/az ./cmd/az
    SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"; \
    LDFLAGS="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=$SHA"; \
    go build -ldflags "$LDFLAGS" -o bin/azd ./cmd/azd

run:
    just build
    DAEMON_BIN="$(pwd)/bin/azd"; \
    if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then \
        AZEDARACH_DAEMON_BIN="$DAEMON_BIN" AZEDARACH_DAEMON_SCOPE=worktree AZEDARACH_DAEMON_SCOPE_SOURCE=just-run ./bin/az daemon restart; \
        AZEDARACH_DAEMON_SCOPE=worktree AZEDARACH_DAEMON_SCOPE_SOURCE=just-run ./bin/az; \
    else \
        AZEDARACH_DAEMON_BIN="$DAEMON_BIN" ./bin/az daemon restart; \
        ./bin/az; \
    fi

bench-git-runtime *ARGS:
    go run ./cmd/bench-git-runtime {{ARGS}}

test:
    go test -v ./...

test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

type-check:
    go build ./...

clean:
    rm -rf bin/ coverage.out coverage.html

install:
    go install ./cmd/az

lint:
    golangci-lint run ./...

check-boundaries:
    if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run --config .golangci-boundary.yml ./internal/...; else echo "WARN: golangci-lint not installed; skipping depguard boundary lint gate" >&2; fi
    ./scripts/check-boundaries.sh
    ./scripts/afv-drift-sentinel.sh
    env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE go test ./internal/tui ./internal/cli
    env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE go test ./internal/daemon/... ./internal/client/...

boundary-check:
    @just check-boundaries

spec-sync:
    ./scripts/spec-sync-precommit.sh

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
