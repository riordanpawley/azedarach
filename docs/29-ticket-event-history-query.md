# Ticket Event History Query Contract

`az ticket events` reads one ticket's durable observation stream through the
daemon. The default remains ascending event-ID order with the existing bounded
default page size. Use `--tail N` for the newest `N` matching events, returned
newest first, or select `--order asc|desc` with `--limit N` explicitly.

## Stable cursor paging

`--after-id` and `--before-id` are exclusive bounds. An ascending page with
more matches exposes `page.next_after_id`; a descending page exposes
`page.next_before_id`. Pass that value to the matching flag for the next page.
Because event IDs are append-only and the cursor excludes the last returned
row, concurrent appends cannot duplicate or skip existing matches.

```sh
az ticket events --order desc --limit 100 --json dkr
az ticket events --order desc --limit 100 --before-id 7120 --json dkr
```

The `issue_events.v2` JSON envelope retains the top-level `events` array and
adds `page` metadata: applied order and limit, first/last IDs, `has_more`, and
the direction-appropriate next cursor.

## Combined filters and search

Event types are ORed within `--type`/`--types`; different filter categories are
ANDed. Exact metadata filters cover source, source command, operation, session,
and worktree. `--since` and `--until` accept a date or RFC3339 timestamp and are
inclusive. ID bounds can be combined with all filters.

`--query` searches the human-facing `summary`, `body`, `message`, `line`, and
`evidence` payload aliases. All normalized query tokens must be present. SQLite
FTS5 selects bounded candidates, followed by the shared domain token matcher so
store and domain semantics remain aligned.

```sh
az ticket events --query 'projection checkpoint' --type progress.recorded dkr
az ticket events --source daemon-orchestration --payload outcome=accepted dkr
```

`--payload key=value` is a repeatable, indexed top-level equality predicate for
`outcome`, `disposition`, `decision_id`, `revision`, and `actor_id`. Values
that are valid JSON are typed (`2`, `true`, `null`, or quoted JSON strings);
other values are ordinary strings. Other payload keys are rejected.
This intentionally avoids an open-ended query language and arbitrary SQL/JSON
expressions while keeping sparse/no-match audit queries index-bounded.

## Storage and performance

Project databases maintain a contentless event-search FTS projection with
insert/update/delete triggers. The migration backfills existing events and adds
issue-scoped ID-order indexes for exact metadata filters. Query-plan regression
tests require the FTS virtual index and metadata indexes on active query paths.
