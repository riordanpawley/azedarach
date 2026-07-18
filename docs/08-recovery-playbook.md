# Recovery Playbook: Revision Mismatch and Partial Failures

## Scope

This playbook covers the daemon `task.bulk.apply` contract used by thin clients.
There is currently no standalone `az apply` CLI subcommand, so validate the
command surface first if you are unsure:

```bash
go run ./cmd/az help
```

For snapshot refresh, the only supported export flags are:

```bash
go run ./cmd/az export --format json [--out <path>]
```

## Before Retrying

Use this flow before any recovery retry:

1. Refresh a current snapshot with `az export --format json`.
2. Read the returned `snapshot_revision`.
3. Rebuild the batch payload from the refreshed snapshot.
4. Retry only after the payload and snapshot revision agree.

Example:

```bash
go run ./cmd/az export --format json --out /tmp/az-snapshot.json
jq -r '.snapshot_revision' /tmp/az-snapshot.json
```

## Revision Mismatch

Revision mismatch means the daemon rejected the batch because the embedded
`snapshot_revision` was stale. The daemon returns:

- `error.code = revision_gap`
- a message in the form `snapshot revision X does not match current revision Y`
- a hard failure at the thin-client layer, which maps to exit code `1`

Recovery steps:

1. Treat the batch as stale, not as partially committed.
2. Refresh the snapshot with `az export --format json [--out <path>]`.
3. Rebuild the batch from the refreshed state.
4. Retry once with the new `snapshot_revision`.
5. If the retry still reports `revision_gap`, stop and refresh again.

Executable checks:

```bash
go test ./internal/daemon/handlers -run TestApplyHandlerRevisionMismatchStopsBeforeExecution -count=1
go run ./cmd/az export --format json --out /tmp/az-snapshot.json
```

## Partial Failure

Partial failure means the daemon accepted the batch, executed some operations,
and reported a non-zero failure count in the response body. The thin-client
wrapper maps this to exit code `2` when:

- the transport response is OK
- the response body decodes successfully
- `summary.failed > 0`

In that case the response body is authoritative. Successful operations have
already been committed, and the daemon preserves input order in `outcomes`.

Recovery steps:

1. Treat the batch as partially committed.
2. Do not replay successful operations.
3. Inspect `summary` and `outcomes` to identify only the failed items.
4. Build a new payload that contains the failed items or the next intended delta.
5. Refresh the snapshot first if the payload was derived from stale state.

Executable checks:

```bash
go test ./internal/cli -run TestResponseExitCode -count=1
go test ./internal/cli -run TestApplyPartialFailureIntegrationPreservesOutcomeOrderAndExitCode -count=1
```

## Exit Semantics

- `0` means success, including dry-run preview responses.
- `1` means hard failure, including transport failure, non-OK responses, or an
  invalid apply response body.
- `2` means partial success with at least one failed operation.

## Operator Notes

- Do not retry a stale payload without refreshing the snapshot.
- Do not reapply operations that already succeeded in a partial batch.
- Keep recovery loops bounded: one refresh, one retry, then stop and inspect.

## Issue State-Model V2 Startup Cutover

The issue database startup migration `0029_issue_state_model_v2` creates a
timestamped SQLite backup before adding v2 lifecycle columns. It records an
`issue:state_model_v2_cutover` marker with the backup path before schema changes
begin and marks the cutover complete only after row validation and version
metadata are committed.

If startup reports a partial or failed state-model v2 cutover:

1. Treat the live issue database as unsafe for automatic repair.
2. Read the error for the recorded backup path and original migration failure.
3. Restore the backup database before retrying startup.
4. Keep the failed live database for inspection until the restored database has
   passed startup and validation.

Do not delete the cutover marker to force startup. The marker refusal happens
before generic schema repair so later migrations cannot run against a partially
upgraded issue schema.

## SQLite Structural Corruption

The issue-store startup path runs `PRAGMA quick_check(1)` before journal-mode,
migration, normalization, or schema-repair writes. A SQLite code-11 result, or
any non-`ok` structural result, quarantines that client and marks the project
issue store unavailable. Runtime code-11 failures from lifecycle or mailbox
paths install the same quarantine and a daemon project-health gate that does
not expire until restart.

When this failure appears:

1. Stop retrying mutations. Do not run repair SQL, `REINDEX`, `VACUUM`, or
   candidate migrations against the original database.
2. Preserve the database and its current `-wal` and `-shm` companions. While
   the daemon is live, obtain a consistent clone with SQLite's online-backup
   API; otherwise stop it cleanly before copying the complete database state.
3. Run `PRAGMA integrity_check` and affected active-path reads only against the
   clone. Map reported root pages through the clone's `sqlite_master` catalog.
4. Recover or salvage only another disposable clone, then prove integrity,
   foreign keys, row preservation, migrations, reopen/idempotency, lifecycle,
   mailbox, orchestration, and representative reads.
5. Replace the authority only through an explicit operator recovery after the
   validated replacement and rollback copy are both preserved. Restarting the
   daemon clears the in-process quarantine but must not be used to bypass a
   still-corrupt database.

Executable regression checks:

```bash
go test ./internal/services/issues -run 'TestClient(QuarantinesRuntimeSQLiteCorruption|RejectsCorruptDatabaseBeforeStartupWrites)' -count=1
go test ./internal/daemon -run TestProjectIssueStoreCorruptionIsCachedAsUnavailableFromAnyStorePath -count=1
```

## Orchestration Integrate Safety Gate

Accepted review completion should normally use
`az orchestrate review accept --root <root> --issue <issue-id>`. The review
decision records trusted acceptance and delegates merge, stopped-session state,
tmux cleanup, worktree removal, and terminal close to the authoritative close
flow before reporting success.

If authoritative close fails after acceptance, repair the reported close
precondition and retry with the same intent key. The accepted reviewer decision
remains authoritative while the operational failure is recorded separately;
the retry resumes close without duplicating acceptance. A ticket reopened after
a successful close is not considered complete from historical evidence: retrying
the same intent closes its current state again.

When evaluating `az orchestrate integrate --issue <issue-id>`, treat it as an
inspection and repair command, not the normal merge authority. Merge/close
guidance is unsafe unless completion evidence exists. The CLI now blocks
completion guidance unless one of these is true:

- the worker issue is already `closed`
- a `worker-integration-ready` mailbox event exists for that worker under its
  parent issue mailbox (`worker-ready` and `worker-complete` are accepted as
  legacy aliases) and its body contains a complete structured
  `worker_evidence.v1` packet

If merge guidance is blocked, recover with:

1. `az issue get <issue-id>` to confirm worker status.
2. `az mail list --parent <parent-issue> --json` to confirm event history.
3. Ask the worker to publish `worker-integration-ready` once evidence is ready
   with a JSON body containing `schema`, `summary`, `commands_run`,
   `key_assertions`, `files_changed`, `review`, `risks`, and optional
   `artifact_links`.
4. Re-run `az orchestrate integrate --issue <issue-id>`.

Use `az branch merge --source <issue-id> --target <ancestor-issue-id|base>` only for manual conflict or close-repair. Use `--source <ancestor> --target <descendant>` when materializing accepted ancestor work into a follow-on worktree; the current worktree is never an implicit target. Root-to-base merges require durable human acceptance recorded on the root issue.

recovery. When it targets a branch that is already checked out in another Git
worktree, the merge runs in that attached worktree. This avoids Git's
single-checkout guard for branches while keeping the target branch as the merge
authority.

Resolve orchestration reviews through the daemon-backed decision command. Run
`az orchestrate review accept --root <root> --issue <review>` only after the
evidence is accepted; it records trusted acceptance and completes authoritative
integration, cleanup, and terminal close before returning success. Use
`az orchestrate review return --root <root> --issue <review> --finding "..."`
for unresolved work. If a decision partially fails, retry it with the same
reported `--intent-key`; do not present dependent results while the accepted
review ticket remains non-terminal.

## Project Orchestrator Recovery

Project orchestrators are daemon-owned exact-scope singletons. Diagnose them
from typed state before touching tmux:

```bash
az orchestrator-session status
az orchestrate status --json
```

For rooted scope, put flags before positionals and pass `--root <issue-id>` to
the orchestrator/orchestrate command. The typed `runtime.reconcile` daemon debug
response (there is no public runtime CLI family) exposes `invariant_sources`.
Verify its mapping with
`go test ./internal/daemon -run TestCommandRuntimeReconcileRoutesToManualRepair`;
`orchestration.scope_singleton` and `orchestration.project_completion` must be
`hybrid`. Projection-only state is not sufficient evidence of live runtime.

- If the lease exists and tmux is live, use declarative `attach`.
- If the lease exists and tmux is absent, use `start`; stale recovery must reuse
  the exact-scope identity and recreate one runtime.
- If work is quiescent, leave the session intact. An unresolved interaction
  prevents completion but does not require a busy worker.
- If lifecycle is `complete-grace`, allow the durable timer to expire. A relevant
  change resets grace; do not pause the session manually to simulate expiry.
- If lifecycle is `paused`, new open work, review, an accepted human answer, or
  recovery wakes it idempotently.

For duplicate/replay symptoms, inspect the durable orchestration cursor and
action key before retrying. Do not invent a new intent key for the same action,
and do not delete SQLite lease/checkpoint rows. Cross-daemon recovery depends on
compare-and-swap and refreshed projections; manual row deletion defeats both.

Interaction recovery is revision-safe. Fetch the current request, preserve the
request ID (not issue ID), and retry with its latest revision. Discussion attach
reuses the daemon-managed advisor singleton. Only a human-confirmed resolution
may apply declared issue/spec/decision effects; expiry and recovery never infer
an answer.
