# Decision Markdown Synchronization

The project SQLite decision store is the live authority. `docs/decisions/` is
the tracked Git exchange format, not a per-commit projection that hooks may
silently refresh.

## Worktree Safety

Linked worktrees share the project decision store but have independent files
and Git indexes. A pre-commit export would therefore read decisions created by
every worktree and could stage them in an unrelated commit. Pre-commit hooks do
not run `az decision sync` and do not implicitly restage `docs/decisions/`.
Configured hook commands and configured restage paths still behave normally.

Post-merge, post-checkout, and post-rewrite hooks continue to run
`az decision import`. Import is conflict-safe: divergent non-empty Markdown and
SQLite fields are reported and skipped. Only an explicit
`az decision import --force` makes Markdown win those conflicts.

## Explicit Exchange Workflow

Before exporting, inspect and import any Git-backed changes in the current
worktree:

```bash
az decision import --check
az decision import
```

Then inspect the full store-to-Markdown reconciliation and apply it deliberately:

```bash
az decision sync --check
az decision sync
git add docs/decisions
```

`az decision sync` is a full reconciliation. It writes the canonical
`<decision-id>-<title-slug>.md` file for every live decision, removes old paths
after title changes and duplicate parseable exports for the same ID, and removes
exports for deleted decisions. Files using the reserved `dec-N[-slug].md`
decision naming convention are reconciled even if their contents are malformed;
other non-decision Markdown files are left untouched. Check mode reports both
writes and removals without changing files.

## Recovery

If Markdown contains changes that are not yet in SQLite, run
`az decision import --check` before sync. Resolve reported conflicts in the
store or Markdown, rerun the check, and import. Use `--force` only when the
reviewed Markdown version is intentionally authoritative. After the store is
correct, run `az decision sync --check`, apply the sync, review the Git diff,
and stage the decision directory explicitly.
