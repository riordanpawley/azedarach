# Configurable issue views

Issue views are daemon-owned projection definitions shared by the normal TUI and
the tmux selector. A definition selects a layout (`column_board`, `tree_list`,
or `horizontal_grid`), first-match groups, conjunctive filters, ordered sort
rules, and display options. The domain projection engine applies those rules;
surface code owns only interaction and rendering.

The supported layouts are exactly Board/Columns (`column_board`), Grid
(`horizontal_grid`), and Tree (`tree_list`). They are properties of saved view
definitions rather than hardcoded application modes. In the TUI, Tab advances
to the next configured view through the daemon selection contract; it does not
toggle a surface-local Compact or Orchestration Overview mode.

The daemon board snapshot schema v5 carries one typed `projection`: ordered
groups reference a single ordered item collection, items carry tree depth, and
`known_task_ids` distinguishes filtered durable issues from tmux-only runtime
discoveries. The TUI and tmux selector render column-board, tree-list, and
horizontal-grid adapters over that same projection. The selector retains live
tmux sessions only when they have no durable issue match. Live tmux discovery
remains authoritative for runtime presence without bypassing view filters.
Each projected item also carries its daemon-derived `orchestration_state`, so
renderers and future view fields can expose readiness, active work, review,
human-decision waits, ownership, and backlog state without recreating
orchestration policy in a client.

Project-orchestrator controls are application chrome, not a board layout. The
status bar shows the current project's orchestrator state when loaded, and the
focused details overlay owns start/attach actions and queue counts.

## Persistence and transient changes

`az board view save` is the explicit persistence boundary for a changed
definition. `az board view select` explicitly changes the selected saved view.
Surface-local layout or sort toggles are transient and do not rewrite a saved
definition.

## Migration

Persisted schema-v1 board definitions remain readable. Decoding assigns the
`column_board` layout and converts the legacy `human_attention` sort policy to
an ordered sort rule. The next explicit save writes schema v2. No SQLite data
migration is required because definitions are decoded at the store boundary.

New definitions use schema v2. The legacy `sort_policy` field remains accepted
for compatibility; `sort` is authoritative when ordered rules are present.
