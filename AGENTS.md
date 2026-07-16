# AGENTS.md

Agent instructions for this repository. This file is the canonical source of agent guidance.

## Project Overview

- Project: Azedarach
- Stack: Go + Bubble Tea + Lip Gloss
- Purpose: TUI Kanban board for orchestrating parallel AI sessions with issue tracking

## Best-Outcome Development Principle

1. Optimize for the strongest durable outcome, not the smallest, fastest, or most locally convenient change. The size of the required implementation, migration, or refactor is not by itself a reason to accept a weaker design.
2. Before committing to an approach, actively look beyond the obvious patch: examine root causes, adjacent constraints, architectural opportunities, and unconventional options that could produce a materially better result.
3. When the best path substantially expands the requested scope, make that expansion and its benefits explicit. Pursue it when it remains within the task's authority; otherwise, ask for the authority or decision needed rather than silently substituting an inferior shortcut.
4. **Hotfix exception:** Apply a speed-first approach only when the user explicitly identifies the task as a hotfix. In that case, prioritize the fastest safe, targeted correction, avoid unrelated scope expansion, and record broader improvements as follow-up work instead of delaying the fix.

## Working Directory

Run commands from repo root:

```bash
cd .
```

## Setup And Validation Commands

Use these as primary checks after code changes:

```bash
# Build and canonical cold test (machine-readable, uncached)
just build
just test

# Run app locally
just run

# Optional focused quality gates
just check-boundaries

# Fast focused development feedback
just test-fast --package ./internal/cli --run TestCommand

# Specialized execution-mode profiles (run when relevant)
just test-integration
just test-migration-clone
just test-race

# Aggregate daemon race sweep (four sequential shards; 15m/shard, 45m aggregate)
just test-race-daemon

# Focused daemon/client boundary checks
./scripts/with-machine-validation-lease --class shared --profile focused-tui-cli -- go test ./internal/tui ./internal/cli
./scripts/with-machine-validation-lease --class shared --profile focused-daemon-client -- go test ./internal/daemon/... ./internal/client/...
./scripts/with-machine-validation-lease --class shared --profile focused-client-boundary -- go test ./internal/tui ./internal/cli ./internal/daemon/...
```

## Test Hang Debugging

- If `just test` appears to "hang" at a package boundary, do not rely on aggregate per-test runtimes alone. Run the suspicious package or test through `./scripts/with-machine-validation-lease --class shared --profile diagnostic -- go test -timeout <short-window> ...` so Go dumps goroutine stacks on timeout without bypassing validation admission.
- Use the stack trace to identify the exact blocked call, then inspect the test fixture for intent/runtime mismatches. Recovery and reconcile tests are especially sensitive to seeding the wrong lifecycle state.
- For session/reconcile flows, model the intended production case explicitly:
  - recovery tests should seed a live tmux runtime with empty daemon cache when validating event publication
  - stop tests should seed desired-stopped intent before runtime cleanup
  - do not seed a desired-stopped row when the assertion is about runtime recovery or reattachment

## Complete Test-Failure Batch Workflow

- Use `just test` (or `just test-timing cold`) for the canonical uncached machine-readable full run. It preserves the complete `go test -json` stream, bounds package concurrency with `-p=4`, emits sorted package/test durations and all failures, and enforces the committed baseline/budgets documented in [docs/26-test-timing-profiles.md](docs/26-test-timing-profiles.md). Use the distinct `cached`, `focused`, `integration`, `migration-clone`, `race`, or `boundary` profiles only for their documented semantics; do not present a focused or cached result as cold full-suite evidence.
- `just merge-gate` is the required local merge contract: build once, run the cold semantic suite once, then run static and focused executable boundary guards. Integration/subprocess, migration-clone, and race profiles are change-sensitive execution-mode gates; they intentionally rerun only the contracts that need those modes.
- When a broad validation command fails, do not fix the first visible test and repeatedly rerun in a one-test-at-a-time loop.
- Run the complete relevant suite once with machine-readable output through the lease authority, such as `./scripts/with-machine-validation-lease --class aggregate --profile diagnostic-full -- go test -json ./... -count=1 -timeout 10m`, and preserve the output outside the repository worktree when it is too large for the terminal. Aggregate-class diagnostics require the orchestrator's validation-capacity assignment.
- Extract the full set of failed tests and packages from that run, then inspect the associated output and classify failures by shared root cause. Truncated terminal output is not evidence that only the visible failure exists.
- Repair each coherent failure class as a batch. Prefer one shared fixture or production-boundary correction when many tests violate the same contract; do not bulk-relax expectations until the intended production semantics are established.
- After the batch repair, rerun the complete suite and collect the next complete failure set. Use focused tests for diagnosis and regression development, but never substitute isolated green tests for the complete rerun.
- If a complete rerun exposes unrelated or flaky failures, reproduce and classify them explicitly. Fix them in scope when appropriate; otherwise create durable follow-up issues with the failing commands and evidence before accepting the scoped change.

## Fast Search Commands

```bash
rg "pattern" --type go internal cmd
fd "filename" -t f internal cmd
```

## Issue Workflow

- Start sessions with `az prime`.
- Use `az issue` for tracked issue operations.
- Track any non-trivial work in issues.
- Do non-trivial implementation in the issue worktree/session, not directly in the main worktree; migrate any accidental main-worktree changes into the issue worktree before cleaning main.

## Spec Documentation Workflow

- Use `az spec read --json` for stored requirement/link data.
- Markdown spec export is disabled until it can export the real stored spec data.
- Keep broader docs in `docs/` aligned with daemon-backed `az spec` records when behavior requirements change.

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
  - [docs/24-issue-state-model-v2-rollout.md](docs/24-issue-state-model-v2-rollout.md)

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

## Azedarach Architecture Placement (Critical)

1. Before adding behavior, inspect the analogous existing path and match its layer boundaries, data flow, naming, and tests.
2. Choose the model-correct path over the locally convenient patch. If the right fix requires changing a shared domain concept, migration, protocol, or invariant, do that deliberately and validate the blast radius instead of preserving an awkward legacy shape because it is easier in the current file.
3. Put durable business semantics in the daemon/domain layer, not in entrypoints, presentation code, handlers, adapters, or ad-hoc SQL. This includes query/search matching rules, lifecycle/status decisions, graph/readiness policy, invariant predicates, and issue/spec relationship rules.
4. Keep stores and migrations responsible for persistence, indexes, ordering, and candidate selection. When storage uses indexes such as FTS, keep the query expression and final semantic filtering aligned with shared domain helpers so indexed behavior cannot drift from domain behavior.
5. Keep daemon handlers and protocol adapters thin: validate transport shape, call application/domain services, and translate typed requests/responses. Do not bury policy decisions there.
6. Keep CLI and TUI as clients: parse flags, render state, and call daemon/client contracts. Do not duplicate daemon/domain logic client-side for convenience.
7. Tests should lock the layer contract: focused domain tests for semantic rules, store/service tests for persistence/index behavior, and active-path tests for CLI/protocol/daemon wiring when user-visible behavior changes.

## Overlay Sizing Contract

1. `View()` must render using `Size()` for geometry. Do not recompute widths or heights in `View()`.
2. `Size()` owns sizing policy. Standard overlays should use a responsive clamp helper; fullscreen or special-case overlays must document why they do not.
3. Validate both the default viewport and a narrow viewport. Standard overlays should stay within bounds and remain readable when space is constrained.
4. Keep golden coverage aligned with the contract: one default-size snapshot and one small-viewport snapshot for standard overlays.
5. Review for anti-patterns: hardcoded dialog widths, duplicated sizing math, `View()` reading terminal size directly, or small-screen behavior left implicit.
6. The canonical checklist lives in [docs/12-overlay-sizing.md](docs/12-overlay-sizing.md).

## Overlay Edit Guardrails

1. Before changing overlay behavior, check `az spec` requirements/links for the active issue and align the spec first. If the work is docs/process-only, note `Spec impact: none (docs/process-only)` in issue notes.
2. Keep `View()` and `Size()` aligned: `View()` consumes `Size()` geometry, and `Size()` owns the responsive policy.
3. After any overlay behavior edit, validate the default viewport and a narrow viewport.
4. If rendered UI output changes, update or add the matching goldens in the same change.

## Thin-Client Boundary Contract (Critical)

1. Daemon owns durable/project lifecycle authority; CLI/TUI are clients for presentation/runtime-ephemeral state.
2. `internal/tui` and `internal/cli` must not directly execute authority operations via `internal/services/{git,tmux,issues,devserver,pr}` when daemon command paths exist.
3. Boundary operations must go through `internal/client/daemonclient` with typed request/response contracts.
4. Session lifecycle must be daemon-authoritative. TUI session maps are projections only.
5. `internal/tui` teardown may stop/clear `SessionMonitor`, but must not call `SessionMonitor.Start` or recreate lifecycle monitoring locally.
6. `m.sessions` mutations must come from daemon snapshot refresh (`projectSessionProjection`), not local authority writes/callbacks.
7. If runtime assets are user-global (socket/lock), preserve project isolation and avoid cross-repo authority bleed.
8. Include regression guards so direct client-side authority operations fail tests if reintroduced.

## Invariant Cache Pattern (Critical)

1. Every daemon invariant must declare an explicit source of truth: `projection`, `tmux`, or `hybrid`.
2. For `projection` and `hybrid` invariants, read pattern must be: refresh in-memory cache from durable SQLite projection first, then evaluate from refreshed in-memory cache.
3. For `tmux` invariants, query tmux directly; do not infer runtime presence only from projection state.
4. `hybrid` invariants must compare durable projection intent and live tmux runtime without fallback shortcuts.
5. Do not evaluate invariants from stale in-memory state or direct ad-hoc SQLite reads.
6. Mutations remain write-through: update in-memory authority and durable projection, then publish events.
7. Example matrix:
   - `session.start`/`session.attach`/`session.pause`/`session.resume`/`session.stop` runtime-presence checks -> `tmux`
   - advisor-session singleton/recovery/cleanup per interaction request -> `hybrid` (refreshed durable request/session-role projection + live tmux runtime; terminal requests and project removal clean advisor resources)
   - session recovery/reconcile -> `hybrid`
   - `session.issue_lifecycle_runtime` live managed-session lifecycle gate -> `hybrid` (refresh durable factored issue disposition/engagement/visibility projection, then compare with live tmux; repair ready+idle to working, reject backlog/terminal/archived divergence without destroying runtime)
   - `session.activity_convergence` stale busy/hooks recovery -> `hybrid` (refresh durable session activity and runtime projections, then compare with a bounded live tmux pane probe; newer durable hook evidence wins races)
   - `session.managed_agent_identity` exact managed-pane and incarnation gate -> `hybrid` (refresh the durable logical-pane, tmux-pane, pane-PID, and agent-incarnation binding, then require an exact live tmux/process match; reused pane IDs and stale hooks fail closed)
   - `task.close`/`task.close_preflight`/`task.delete`/`task.delete_preflight`/`task.graph_readiness`/`task.complete_check` durable lifecycle, investigation disposition/acceptance, and orchestration checks -> `hybrid` (read v2 issue lifecycle and evidence projection first, then compare with live runtime)
   - `task.review_handoff` material-decision acknowledgement and external busy-equivalent session activity gates before moving to `in_review` -> `projection` (durable issue v2 lifecycle/review projection + revisioned decision change/acknowledgement observations + session activity projection; active issue self-handoff remains allowed only after material decisions are current)
   - `decision.propagation_delivery` integration-affecting decision fanout and worker wake retry -> `hybrid` (atomic durable decision audit/outbox + per-issue materialization checkpoint and authoritative exact-revision acknowledgement projection, reconciled with live tmux delivery until acknowledged; superseded and withdrawn revisions remain silent)
   - `task.integration_readiness` worker evidence gate and `task.context_risk_closeout` repeated-local-failure gate -> `projection` (durable issue projection + mailbox/observation evidence; integration readiness also requires completed non-overlapping aggregate evidence bound to the clean exact candidate revision)
   - `task.merge_base_target` branch integration target gate -> `projection` (durable issue graph + worktree projection; explicit root-to-base requests also require issue-scoped `human.input_provided` acceptance evidence)
   - `task.follow_on_merge_candidates` follow-on merge source gate -> `projection` (durable issue graph + worktree projection)
   - `orchestration.project_candidates` bounded project candidate classification -> `projection` (durable issue graph/lifecycle, ownership, session activity, and interaction projections)
   - `orchestration.project_review` review queue, reviewer lease, structured evidence, and outcome gate -> `hybrid` (refresh durable issue/review/ownership and exact issue-worktree projections, then verify live Git worktree identity, clean HEAD, and exact aggregate revision before acceptance; accepted close delegates to the existing hybrid `task.close` invariant)
   - `orchestration.claim_start` bounded worker-wave claim/start and compensation -> `hybrid` (durable ownership/start-attempt projection + daemon session-start operation/runtime)
   - `orchestration.project_loop` durable watch cursor, deterministic action replay, and non-blocking start/review scheduling -> `projection` (durable issue observation stream + orchestration checkpoint refreshed before each loop decision; queued review stays visible without globally suppressing unrelated starts)
   - `issue_resources.lifecycle` issue resource desired-state gate -> `projection` (durable issue status + runtime attachment projection)
   - `interaction.waiting_human` decision-waiting and pickup exclusion gate -> `projection` (durable interaction request projection refreshed before evaluation)
   - `investigation.waiting_human` human-findings authority gate -> `projection` (durable investigation disposition and issue-specific acceptance/review evidence refreshed before evaluation)
   - `interaction.staleness` stale/reminder/disposition/recovery policy -> `projection` (durable interaction request projection refreshed before age evaluation and revision-safe write-through audit)
   - `decision.markdown_transfer_target` decision Markdown import/export scope -> `hybrid` (refresh durable worktree-to-issue projection, then compare it with the live Git worktree path and HEAD revision; issue worktrees transfer only decisions carrying that issue provenance)
   - task-list freshness/session projection checks -> `projection` via refresh-then-cache
   - cross-project configurable views and tmux selector ordering -> `projection` (global-daemon-owned user database incrementally consumes verified per-project issue deltas plus independently keyed runtime/fact materializations; full export is bootstrap/explicit rebuild/isolated recovery only; scoped issue/session/worktree/dependency keys and explicit vector/stale/unavailable project health)
   - orchestration scope identity -> `projection` (durable project + typed rooted/project scope)
   - orchestration scope singleton -> `hybrid` (refreshed durable scope lease + live tmux runtime)
   - rooted parent orchestration continuation -> `hybrid` (durable rooted lease/cursor + refreshed direct nested-root, interaction, completion, and session projections + live tmux wake delivery)
   - project orchestration completion -> `hybrid` (refreshed issue/review/interaction/session projections + live tmux runtime)
   - keyed monotonic projection delta replay, consumer cursors, and snapshot-at-cursor reads -> `projection` (durable project delta ledger and version history; reads never reconcile or poll tmux)
   - asynchronous tmux current-state observation -> `tmux` (one daemon-owned bounded observer polls inventory and sparse pane classifications, publishes only changed disposable current-state projections with external-observation provenance, and never admits `session.runtime_observed` or advances a semantic sequence)
   - repository-family validation capacity -> `projection` (durable daemon validation request/lease queue refreshed transactionally before aggregate/shared/safe admission; heartbeat expiry is recovery authority)

### Adding New Invariants (Required Checklist)

1. Add invariant ID and source policy (`projection`/`tmux`/`hybrid`) in the shared daemon invariant matrix.
2. Route invariant evaluation through existing policy-aware helpers (no ad-hoc direct source reads).
3. Add/update runtime debug visibility so `runtime.reconcile` exposes the invariant source mapping.
4. Add regression tests:
   - source-matrix mapping test coverage
   - behavior test for the concrete invariant path
   - multi-daemon stale-cache race test when lifecycle state could diverge across processes
5. Update docs (`AGENTS.md` and `docs/README.md`) when adding or changing invariant sources.

## Architecture Migration Gates

1. Do not mark migrations complete while active entrypoints still depend on transitional adapters or legacy execution paths.
2. Runtime acceptance criteria must prove the intended production path is wired on active entrypoints, not only isolated package/tests.
3. Cross-process boundaries must use typed protocol/domain payloads, not UI-framework message types.

## Database Migration Safety Gate (Critical)

1. For any migration, schema ensure/repair logic, trigger/index change, persistence-authority change, migration failure, or pre-merge review containing database changes, use `$database-migration-review` at [.codex/skills/database-migration-review/SKILL.md](.codex/skills/database-migration-review/SKILL.md).
2. Default to exactly one new migration per merge to main, or one per independently versioned database authority when a merge genuinely changes more than one. Consolidate branch-local steps before clone testing; never squash or mutate migrations already merged or executed against any real database.
3. Pre-merge review must test the candidate through real startup/store paths against safe temporary clones of the root user database and every registered project database. Never test candidate migrations on the originals.
4. Require fresh, historical-upgrade, idempotent-reopen, rollback, drift, and real-database-clone evidence. Fresh-database tests alone are insufficient.
5. Migration changes remain high risk and require three clean review passes after the final migration-affecting edit.
6. Every migration ID must have exactly one embedded immutable artifact and a pinned SHA-256 checksum. Go-assisted migrations require a SQL/manifest artifact describing schema, data, validation, and ledger effects; callbacks may orchestrate but never replace the artifact.
7. Registration must fail for duplicate IDs, missing/empty artifacts, checksum drift, or a registry/artifact mismatch. Applied ledger rows must record `artifact_checksum`; legacy rows may be backfilled once from the pinned catalog and must reject later mismatch.

## Active-Path Placeholder Policy

- Placeholder implementations are allowed only when off active runtime paths or explicitly tracked as incomplete follow-up work.
- If an active-path placeholder remains, keep the issue partial/in-progress and link follow-up issue IDs.

## Closure Evidence (Required)

For architecture or boundary work, only close when notes include:

1. Commands run
2. Key outputs/assertions
3. Files changed
4. Explicit AC pass/fail checklist

If any are missing, keep issue state `in_progress` or `open`.

## Investigation Review Gate (Required)

1. Use the durable `investigation` issue type for research, discovery, spikes, analysis, experiments, audits, and issues whose primary deliverable is findings, options, recommendations, or an AI Agent Band/session discussion, even when they have no code diff or repository artifact.
2. Whenever issue intent and type disagree, correct the type with `az issue update --type <type>` and record why. Until corrected, apply this gate based on actual intent so legacy or misclassified work is not closed accidentally.
3. Investigations default to the `human_findings` disposition for migration safety. Human-facing investigations require explicit, issue-specific `human.input_provided` evidence with `investigation_findings_accepted=true` before integration, terminal close, cancellation, session stop, or worktree cleanup. An integration sweep, `in_review`, reviewer approval, or lack of a diff is insufficient.
4. AI-initiated read-only review/audit children may instead declare `internal_review` with an `investigation.disposition_declared` issue record when their findings are intermediate verification inputs consumed by implementation. They may close only after a durable orchestration `review.completed` outcome of `accepted`; a later `returned` or `integration_failed` outcome revokes readiness until a new acceptance.
5. Before asking for human review of `human_findings`, surface the findings and identify all artifacts plus the relevant Agent Band/session location. Preserve the issue, session, and worktree in review/waiting-human state and record durable evidence that human review is pending.
6. Immediately before any terminal action, re-check the issue's type, disposition, and durable evidence. Never infer `internal_review` from title, parentage, read-only work, or agent authorship; missing/invalid declarations remain human-facing.

## CLI/Binary Rules

1. In this repo, PATH `az` is the Go implementation.
2. Treat the installed/root `az` and `azd` binaries as production runtime assets. Worktree validation must not replace, overwrite, restart, or intentionally version-mismatch the production daemon.
3. Normal issue work must use one global daemon and one authority path. Linked worktree CLI commands must use the user-global daemon socket/lock by default; they must not autostart a worktree-scoped daemon for ordinary `az issue`, `az session`, `az branch`, or similar workflow commands.
4. Use `AZEDARACH_DAEMON_SCOPE=worktree` only when intentionally testing daemon/runtime behavior from an Azedarach development worktree. In that explicit mode, `go run ./cmd/az ...` may autostart a worktree-scoped daemon using the worktree-scoped socket/lock and the same worktree's `go run ./cmd/azd` source fallback when no worktree-local `bin/azd` exists.
5. For validation in a worktree, prefer `go run ./cmd/az ...` from that worktree. If a compiled binary is needed, build to a worktree-local scratch path such as `.tmp/az-test/az`, and run that exact path. Do not use `bin/az`, which is a production runtime path in the primary worktree.
6. Do not copy worktree-built `az`/`azd` into `/Users/riordan/prog/azedarach/bin`, `/usr/local/bin`, Homebrew paths, or any shared PATH location unless the user explicitly asks for a production install/deploy.
7. Do not run `az daemon restart`, `az daemon stop`, or `az daemon start` from a linked worktree as a validation shortcut. Use a worktree-scoped daemon path (`AZEDARACH_DAEMON_SCOPE=worktree go run ./cmd/az daemon restart`) only when the test specifically requires a live daemon restart. From the main repo, `az daemon restart` is allowed only when validating the production/root daemon path.
8. If logs show `daemon version mismatch persisted after replacement`, assume a worktree binary has interacted with the shared production daemon. Stop and fix the isolation path or guidance; do not keep retrying replacement/restart commands.
9. Do not bump protocol/version only to force restarts; bump versions only for contract changes.
10. Keep CLI docs/help/examples with flags before positional arguments.
11. Ordinary validation and orchestration may run `just build`; it must remain
    isolated to `.tmp/az-test` and must not create, replace, or delete
    `bin/az`/`bin/azd`. Automated agents and project orchestration must not run
    `just build-install-run` unless the user explicitly requests a production
    rebuild/relink from the primary worktree, because that recipe intentionally
    installs a serialized, atomically switched `az`/`azd` generation and rejects
    linked worktrees. Installed command links must resolve only inside the stable
    install directory and never back into any Git worktree.
12. Repository direnv setup must not prioritize primary- or linked-worktree
    `bin/` directories. It must preserve inherited multipurpose PATH entries,
    select the installer-owned active control links, and prepend their
    symlink-resolved immutable generation so stale `az`/`azd` cannot win without
    hiding unrelated tools; matching version text alone is not trusted. Global
    daemon launch/replacement resolves `azd` beside the
    running `az` only when its resolved identity is
    `.azedarach-generations/generation.*/az`, and fails closed when managed
    identity or its sibling is unavailable. Canonical primary-repo `bin/`
    binaries are not trusted production assets; bare PATH, repo-local binary,
    and source fallback are reserved for explicit worktree-scoped development.
    Successful install generations remain retained until an explicit
    lifetime-aware maintenance contract can prove they are no longer needed by
    long-lived clients.

## Environment Rules

- `.envrc` stores one shared direnv/nix-direnv layout per repository under `${XDG_CACHE_HOME:-$HOME/.cache}/direnv/layouts`, keyed by the canonical Git common directory, so linked worktrees reuse the warm profile without gaining checkout-local `.direnv` state. Run `nix-direnv-reload` after changing `flake.nix` or `flake.lock`.
- `.envrc` keeps `GOPATH` and module downloads shared under the primary repo's `.azedarach/go/path`, but assigns build output to `.azedarach/go/caches/v1/<normal|race|coverage>/<main|issue-ID>`. It exports `-trimpath` through `GOFLAGS`; `AZEDARACH_GO_CACHE_ROOT` and `AZEDARACH_GOCACHE` may only restate their derived daemon-authoritative locations and are rejected when they redirect outside the project layout. `AZEDARACH_GOPATH` remains an explicit local module-download override.
- Managed validators hold the repository-family Go cache lock shared while supported cache maintenance holds it exclusively. Cache accounting defaults to a 10 GiB soft warning plus a 28 GiB hard refusal. Override bytes with `AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES` and `AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES`; opt into selected-namespace maintenance with `AZEDARACH_GO_CACHE_AUTO_MAINTAIN=1`.
- Canonical `just merge-gate` holds the daemon-owned repository-family aggregate validation lease across its complete build/test/boundary sequence. Start builds and named test profiles through `just`; wrap any necessary raw `go test`, `go build`, `go run`, benchmark, race, or diagnostic command with `scripts/with-machine-validation-lease` so it queues before invoking Go. Aggregate admission waits for observed unleased Go work to quiesce, samples the complete leased command, and refuses overlapping evidence. Inspect live ownership and waiters without compiling Go via `just validation-status` or `just validation-watch`.
- Production installation from the primary `main` worktree outranks development validation admission. `just build-install-run` must serialize against another production installer, but it must never wait for active shared or aggregate validation. A validation that overlaps a production install is noncanonical and must be rerun; production build, atomic install, daemon restart, and TUI availability proceed normally.
- Use `just go-cache-inventory` before legacy cleanup. `just go-cache-clean-legacy --confirm` uses supported Go cleanup for legacy build caches; add `--include-gopath-modcache` only to remove the legacy module download cache. It never removes legacy GOPATH binaries or other user files.
- After `direnv allow`, use normal `go ...` commands from repo root without per-command env prefixes.

## Git Safety Rules

1. When already in target repo/worktree, use plain `git` commands.
2. Use local-only git flow by default; do not run remote sync/cleanup commands unless explicitly requested.
3. Never delete untracked files or run `git restore` without explicit permission.
4. Never finalize merges into `main` with `--no-verify`; resolve failing hooks/checks first (or stop and ask the user).

## Pre-Completion Review Gate

Before declaring implementation work done, moving an issue to `in_review`, or handing off code changes, run `$code-review-loop`.

The review loop must review the actual change set, fix actionable findings, validate, and repeat until it reaches its clean-pass target. Do not wait for the user to request a separate code review/fix/re-review cycle.

Skip this gate only for work that did not change code or executable behavior, such as pure explanation, read-only investigation, or issue-tracker updates. If docs, tests, config, scripts, or tooling changed in a way that can affect users or developers, run the gate.

## Session Completion Checklist

1. File follow-up issues.
2. Run relevant quality gates.
3. Update issue status.
4. Confirm `git status`.
5. Ensure intended changes are committed locally.
