# List available commands
default:
    @just --list

build-link-run:
    ./scripts/build-link-run.sh

build:
    go build -o bin/az ./cmd/az
    go build -o bin/azd ./cmd/azd

run:
    go build -o bin/az ./cmd/az
    go build -o bin/azd ./cmd/azd
    AZEDARACH_DAEMON_SCOPE=worktree ./bin/az daemon restart
    AZEDARACH_DAEMON_SCOPE=worktree go run ./cmd/az

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
    env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE go test ./internal/app ./internal/cli
    env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE go test ./internal/daemon/... ./internal/client/...

boundary-check:
    @just check-boundaries

spec-sync:
    ./scripts/spec-sync-precommit.sh

release-homebrew *ARGS:
    ./scripts/release-homebrew.sh {{ARGS}}
