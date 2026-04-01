# AGENTS.md

Agent instructions for this repository. This file is the canonical source of agent guidance.

## Project Overview

- Project: Azedarach
- Stack: Go + Bubble Tea + Lip Gloss
- Purpose: TUI Kanban board for orchestrating parallel AI sessions with issue tracking

## Working Directory

Run commands from repo root:

```bash
cd .
```

## Setup And Validation Commands

Use these as primary checks after code changes:

```bash
# Build and test
just build
just test

# Run app locally
just run

# Optional focused quality gates
just check-boundaries

# Full Go test sweep
go test ./...

# Focused daemon/client boundary checks
go test ./internal/tui ./internal/cli
go test ./internal/daemon/... ./internal/client/...
go test ./internal/tui ./internal/cli ./internal/daemon/...
```

## Fast Search Commands

```bash
rg "pattern" --type go internal cmd
fd "filename" -t f internal cmd
```

## Issue Workflow

- Start sessions with `az prime`.
- Use `az issue` for tracked issue operations.
- Track any non-trivial work in issues.

## Spec Documentation Workflow

- Treat spec-synced Markdown under `docs/spec/` as the spec source of truth when present.
- Keep broader docs in `docs/` aligned with those spec files.
- Pre-commit runs `scripts/spec-sync-precommit.sh`; when spec sync is configured, it auto-stages `docs/spec/` updates.
- Configure one explicit sync command via `AZ_SPEC_SYNC_CMD` when your environment provides spec tooling.

## Developer Docs Map

- [docs/README.md](docs/README.md) is the index for developer/internal docs and audience notes.
- Core architecture references:
  - [docs/01-overview.md](docs/01-overview.md)
  - [docs/02-architecture.md](docs/02-architecture.md)
  - [docs/03-project-structure.md](docs/03-project-structure.md)
- Boundary and runtime authority references:
  - [docs/06-daemon-battle-tested-path.md](docs/06-daemon-battle-tested-path.md)
  - [docs/07-daemon-package-boundaries.md](docs/07-daemon-package-boundaries.md)
  - [docs/09-boundary-hardening-policy.md](docs/09-boundary-hardening-policy.md)
  - [docs/adr/1-daemon-ownership-adr.md](docs/adr/1-daemon-ownership-adr.md)
- Operational references:
  - [docs/08-recovery-playbook.md](docs/08-recovery-playbook.md)
  - [docs/10-go-release-and-homebrew.md](docs/10-go-release-and-homebrew.md)

## Go/Bubbletea Engineering Rules

1. Keep entrypoint wiring in `cmd/`, private implementation in `internal/`, and docs in `docs/`.
2. Accept interfaces at boundaries and return concrete structs from constructors.
3. Use functional options for optional constructor configuration.
4. Keep Bubble Tea models nested with explicit message routing and explicit shared state.
5. Batch startup/init commands with `tea.Batch(...)`.
6. Pass `context.Context` as first parameter for I/O or long-running work.
7. Wrap errors with context (`fmt.Errorf("...: %w", err)`) and prefer `slog` for structured logs.
8. Keep tests deterministic, table-driven, and mock via interfaces.
9. Prefer context cancellation for goroutine lifecycle and buffered channels when capacity is known.

## Thin-Client Boundary Contract (Critical)

1. Daemon owns durable/project lifecycle authority; CLI/TUI are clients for presentation/runtime-ephemeral state.
2. `internal/tui` and `internal/cli` must not directly execute authority operations via `internal/services/{git,tmux,issues,devserver,pr}` when daemon command paths exist.
3. Boundary operations must go through `internal/client/daemonclient` with typed request/response contracts.
4. Session lifecycle must be daemon-authoritative. TUI session maps are projections only.
5. `internal/tui` teardown may stop/clear `SessionMonitor`, but must not call `SessionMonitor.Start` or recreate lifecycle monitoring locally.
6. `m.sessions` mutations must come from daemon snapshot refresh (`projectSessionProjection`), not local authority writes/callbacks.
7. If runtime assets are user-global (socket/lock), preserve project isolation and avoid cross-repo authority bleed.
8. Include regression guards so direct client-side authority operations fail tests if reintroduced.

## Architecture Migration Gates

1. Do not mark migrations complete while active entrypoints still depend on transitional adapters or legacy execution paths.
2. Runtime acceptance criteria must prove the intended production path is wired on active entrypoints, not only isolated package/tests.
3. Cross-process boundaries must use typed protocol/domain payloads, not UI-framework message types.

## Active-Path Placeholder Policy

- Placeholder implementations are allowed only when off active runtime paths or explicitly tracked as incomplete follow-up work.
- If an active-path placeholder remains, keep the issue partial/in-progress and link follow-up issue IDs.

## Closure Evidence (Required)

For architecture or boundary work, only close when notes include:

1. Commands run
2. Key outputs/assertions
3. Files changed
4. Explicit AC pass/fail checklist

If any are missing, keep issue state `in_progress` or `blocked`.

## CLI/Binary Rules

1. In this repo, PATH `az` is the Go implementation.
2. For validation, use `go run ./cmd/az ...`, `go run ./cmd/azd ...`, or `./bin/az ...` from repo root.
3. For daemon restarts, use `az daemon restart` from repo root (`just run` does this automatically).
4. Do not bump protocol/version only to force restarts; bump versions only for contract changes.
5. Keep CLI docs/help/examples with flags before positional arguments.

## Environment Rules

- `.envrc` exports repo-local `GOCACHE`/`GOPATH` (`.gocache`, `.gopath`).
- After `direnv allow`, use normal `go ...` commands from repo root without per-command env prefixes.

## Git Safety Rules

1. When already in target repo/worktree, use plain `git` commands.
2. Use local-only git flow by default; do not run remote sync/cleanup commands unless explicitly requested.
3. Never delete untracked files or run `git restore` without explicit permission.
4. Never finalize merges into `main` with `--no-verify`; resolve failing hooks/checks first (or stop and ask the user).

## Session Completion Checklist

1. File follow-up issues.
2. Run relevant quality gates.
3. Update issue status.
4. Confirm `git status`.
5. Ensure intended changes are committed locally.
