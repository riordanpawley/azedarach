# Recovery Playbook: Revision Mismatch and Partial Failures

## Scope

This playbook covers the daemon `task.bulk.apply` contract used by thin clients.
There is currently no standalone `az apply` CLI subcommand, so validate the
command surface first if you are unsure:

```bash
go run ./cmd/az help
```

The current user-facing CLI surface is:

- `start`
- `attach`
- `kill`
- `status`
- `export`
- `help`

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
