# az spec v1 Contract (Phase 1)

Status: Active implementation contract for epic `bfs`  
Scope: Go runtime restoration of `az spec` commands with daemon-authoritative execution

## Goals

- Restore a deterministic `az spec` command family in Go runtime.
- Route authority operations through daemon protocol/client contracts (no direct CLI-side store mutation).
- Define strict parse/validation/output rules so CLI, daemon, and tests align.

## Current Slice Status

- CLI grammar and help text are restored for the phase-1 command surface.
- `az spec sync --target md` is implemented in the Go CLI layer and generates deterministic `docs/spec/` Markdown.
- `req`, `link`, `read`, `lint`, and `parity` still require daemon-backed authority contracts for real execution; until then they validate syntax and fail with a consistent not-implemented error.

## Command Surface (Phase 1)

`az spec` subcommands in scope:

- `req`
- `link`
- `read`
- `lint`
- `parity`
- `sync --target md`

`az --help` must list `spec` and `az spec --help` must list all subcommands above.

## Global CLI Rules

1. Deterministic argument ordering: flags/options before positional arguments.
2. Unknown subcommands/flags fail with non-zero status and usage text.
3. `--json` output, when supported by a subcommand, must emit valid JSON only (no banner/prefix text).
4. When `spec.enabled=false`, all `az spec ...` commands fail fast with explicit remediation:
   - Message includes `az config set spec.enabled true`.
5. CLI is transport/presentation only; daemon is authoritative for reads/writes.

## Grammar

### Requirement commands

1. `az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--id <req-id> ...] [--ids a,b,c]`
2. `az spec req get --id <req-id> [--json]`
3. `az spec req create --id <req-id> --title <text> [--description <text>] [--issue <issue-id>] [--json]`
4. `az spec req update --id <req-id> [--title <text>] [--description <text>] [--status <open|accepted|superseded>] [--json]`
5. `az spec req delete --id <req-id> --confirm [--json]`

### Link commands

1. `az spec link list [--json] [--issue <issue-id>] [--req <req-id>] [--id <link-id> ...] [--ids a,b,c]`
2. `az spec link add --issue <issue-id> --req <req-id> [--role <implements|verifies|relates>] [--note <text>] [--json]`
3. `az spec link remove --issue <issue-id> --req <req-id> [--json]`

### Read/lint/parity/sync commands

1. `az spec read [--json] [--issue <issue-id>] [--req <req-id>]`
2. `az spec lint [--json] [--strict]`
3. `az spec parity [--json] [--fail-on-out]`
4. `az spec sync --target md [--check] [--json]`

### Alias policy

- No shorthand aliases in phase 1 other than existing top-level CLI alias behavior.
- Parser rejects unknown aliases; tests cover explicit rejection.

## Selector and Identifier Validation

1. `issue-id` must be non-empty trimmed string.
2. `req-id` must be non-empty trimmed string.
3. Repeated selectors (`--id`, `--ids`) preserve caller order in JSON/plain output where ordering is meaningful.
4. `--ids` comma list ignores surrounding whitespace and drops empty tokens.
5. Duplicate selector tokens are de-duplicated while preserving first occurrence.

## Output and Error Determinism

1. Plain output tables use stable column order and deterministic row ordering.
2. JSON output uses stable key names and deterministic array ordering.
3. Success output never mixes with warnings/errors in JSON mode.
4. Validation errors return actionable text containing the failing field and expected format.
5. Daemon unavailability returns consistent transport error envelope surfaced by CLI.

## Daemon Authority Invariants

1. CLI `az spec` write/read actions execute via daemonclient typed requests/responses.
2. Daemon handlers own DB transactions and validation for spec mutations.
3. No direct `internal/tui` or `internal/cli` store mutation paths for spec writes.

## Audit and Mutation Requirements

1. Requirement and link mutations must emit SQLite audit rows.
2. Audit writes are in the same transaction as primary mutation.
3. Failure to write audit row fails the mutation.

## Acceptance Matrix by Leaf

1. `bgc` / `bfu`: contract completeness
   - Grammar in-repo.
   - Deterministic output/error rules in-repo.
   - Phase-1 non-goals explicit.
2. `bfv` / `bgd`: schema + migration
   - Fresh DB includes spec tables and audit tables.
   - Legacy DB migrates forward without data loss.
3. `bfw` / `bge`: daemon protocol + handlers
   - Typed protocol messages for req/link/read/lint/parity/sync.
   - Handler tests cover happy-path and validation failures.
4. `bfx` / `bgg`: daemon client typed methods
   - Client methods map 1:1 to daemon commands.
   - Request/response decode tests pass.
5. `bfy` / `bgh`: CLI family
   - `az spec` appears in help.
   - Strict parsing tests for ordering/selectors.
   - JSON/plain deterministic output tests.
6. `bgl` / `bgm` / `bgn`: audit implementation
   - Mutation commands assert audit row creation.
   - Query surfaces required metadata for audit inspection.
7. `bgb`: end-to-end gate
   - End-to-end command suite green for phase-1 matrix.

## Phase-1 Non-goals

1. No Linear publish integration.
2. No AI-assisted requirement generation.
3. No multi-target sync besides `--target md`.
4. No speculative client-side caching of spec authority data.

## Closure Evidence Requirements

When closing any phase-1 leaf, include:

1. Commands run.
2. Key outputs/assertions.
3. Files changed.
4. AC checklist pass/fail.
