# Configurable issue views

Issue views are daemon-owned projection definitions shared by the normal TUI and
the tmux selector. A definition selects a layout (`column_board`, `tree_list`,
or `horizontal_grid`), first-match groups, conjunctive filters, ordered sort
rules, and display options. The domain projection engine applies those rules;
surface code owns only interaction and rendering.

User-level views may scope themselves across all registered projects. They are
stored in the user database and evaluated against its project-scoped SQLite
projection. Global results use `(project_id, issue_id)` identities throughout;
bare issue IDs are never sufficient across projects. Per-consumer selections
allow the global board and tmux selector to choose different defaults while
reusing the same definitions and evaluator.

The supported layouts are exactly Board (`column_board`), Grid
(`horizontal_grid`), and Tree (`tree_list`). They are properties of saved view
definitions rather than hardcoded application modes. In the TUI, Tab advances
to the next configured view through the daemon selection contract; it does not
toggle a surface-local Compact or Orchestration Overview mode.

Press `V` in normal mode to open **Views**. Create and edit open the **View
Configurator**, whose guided fields cover title, Grid/Board/Tree layout,
filters, grouping/columns, ordered sorting, and display options. Built-ins are
marked immutable in context; duplicate one with `d` before editing it. The
advanced JSON editor remains available with `Ctrl+J`, but is never required by
the primary TUI workflow. `V` is deliberately scoped to normal mode; the
existing select-mode dev-server binding does not conflict.

The project-local daemon board snapshot schema v5 carries one typed `projection`: ordered
groups reference a single ordered item collection, items carry tree depth, and
`known_task_ids` distinguishes filtered durable issues from tmux-only runtime
discoveries. The TUI and tmux selector render column-board, tree-list, and
horizontal-grid adapters over that same projection. The selector retains live
tmux sessions only when they have no durable issue match. Live tmux discovery
remains authoritative for runtime presence without bypassing view filters.
Each projected item also carries its daemon-derived `orchestration_state`, so
renderers and future view fields can expose readiness, active work, review,
human-decision waits, ownership, and backlog state without recreating
orchestration policy in a client. Global snapshots carry the equivalent scoped
projection and explicit per-project freshness, including stale or unavailable
projects whose last good rows are retained.

Project-orchestrator controls are application chrome, not a board layout. The
status bar shows the current project's orchestrator state when loaded, and the
focused details overlay owns start/attach actions and queue counts.

## Persistence and transient changes

`az view create` and `az view update` are the explicit persistence boundaries
for definitions. `az view select` explicitly changes the selected saved view.
Surface-local layout or sort toggles are transient and do not rewrite a saved
definition.

`az view list|get|select|create|update|delete|explain` is the canonical CLI
information architecture. `az board view ...` remains a compatibility alias
with identical request and persisted-definition behavior for existing
automation.

Use `--project global` to manage user-level cross-project definitions. Global
definitions persist one of `all_projects`, `selected_projects`, or
`current_project` scopes; selected/current scopes take canonical IDs through
`--scope-projects`. Consumer defaults are independent (`global_board`,
`tmux_selector`, `search`, and `review`). For example:

```bash
az view create --project global --file view.json --scope selected_projects --scope-projects alpha,beta
az view select --project global --consumer tmux_selector orchestration
az view get --project global --json orchestration
```

The tmux selector status line identifies its selected saved view and prints the
`az view select --project global --consumer tmux_selector VIEW` command used to
change it. Arrow keys, `h/j/k/l`, and the mouse wheel move the active item;
Page Up and Page Down move by the visible page while keeping the active session
on screen in Grid, Board, and Tree layouts.

`az global` opens the cross-project TUI. Its navigation keys operate on scoped
`(project_id, issue_id)` identities. Before opening an issue or offering a
project mutation, the TUI switches to and hydrates that issue's authoritative
project snapshot; projection-only synthetic keys never cross the daemon
contract.

## Migration

Persisted schema-v1 board definitions remain readable. Decoding assigns the
`column_board` layout and converts the legacy `human_attention` sort policy to
an ordered sort rule. The next explicit save writes schema v2. No SQLite data
migration is required because definitions are decoded at the store boundary.

New definitions use schema v2. The legacy `sort_policy` field remains accepted
for compatibility; `sort` is authoritative when ordered rules are present.
