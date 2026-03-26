# TS vs Go Parity Audit (2026-03-26)

## Scope

Issue: `aya` ("compare every feature in ts impl to go impl")

This audit compares:
- TypeScript implementation (`ts-opentui`) behavior documented in:
  - `ts-opentui/docs/keybindings.md`
  - `ts-opentui/docs/README.md`
  - `az --help` (PATH `az`, the TS-linked CLI)
- Go implementation (`go-bubbletea`) behavior evidenced in:
  - `go-bubbletea/internal/app/model.go`
  - `go-bubbletea/internal/ui/overlay/*.go` (action/help/settings/task workspace)
  - `go run ./cmd/az --help`

## Executive Summary

- CLI parity is **not close**: TS CLI exposes broad issue/spec/project/dev/hook workflows; Go CLI currently exposes a narrower command surface.
- TUI board navigation parity is **partial**: core movement/modes exist in Go, but many TS action workflows are missing or changed.
- Task workspace model differs materially:
  - TS uses `Space` + action key from board.
  - Go uses `Space` to open a Task Workspace overlay with detail/actions panes.
- Keybinding parity is **mixed**:
  - Core navigation keys match (`h/j/k/l`, arrows, `g` prefixed goto, `v`, `?`, `,`, `f`, `/`, `Tab`).
  - Several documented TS actions have no Go equivalent yet (`Space+!`, `Space+H`, `Space+M`, `Space+O`, `Space+T`, etc.).
  - Go has some different semantics (`w/W` cleanup in action menu vs TS `d/D` family).

## Detailed Differences

## 1) CLI Feature Surface

TS `az --help` includes broad command groups beyond session + issues:
- `config`, `prime`, `sync`, `gate`, `dev`, `notify`, `hooks`, `project`, `opencode`, extensive `spec` workflow.

Go `go run ./cmd/az --help` currently includes:
- TUI entrypoint, `session`, `issue` CRUD/deps/bulk, `prime`, `export`, `daemon restart`, `help`.

Net gap:
- Missing Go equivalents for major TS command families (especially `spec`, `hooks`, `opencode`, broader project/dev operations).

## 2) Board-Level Modes and Navigation

Present in both (evidence: TS keybindings doc + Go `handleNormalMode`/`handleGotoMode`):
- `h/j/k/l` + arrow navigation
- `g` mode with `gg/ge/gh/gl`
- `gw` jump labels
- `gp` project selector
- `gs` spec workspace
- `/` search, `f` filter, `,` sort, `v` select, `?` help
- `Tab` board/compact toggle

Differences:
- TS docs advertise `Ctrl-l` redraw; Go help text advertises it but `handleNormalMode` currently has no `ctrl+l` case.
- TS README describes `Enter` as detail-or-epic-drill; Go `Enter` in normal mode is epic drill-only, with non-epic feedback toast directing to `Space` task workspace.

## 3) Task Action Model

TS (from keybindings doc):
- Action mode is a board prefix (`Space`, then action key).

Go (from `handleNormalMode` + `TaskWorkspaceOverlay` + `ActionMenu`):
- `Space` opens a Task Workspace overlay.
- Actions are executed from overlay (shortcut keys still work while overlay is open).

Meaning:
- User interaction flow is intentionally different even when actions have similar names.

## 4) Session Actions

TS documented set includes `Space+s/S/!/a/p/r/Ctrl+r/R/x`.

Go current state (from `handleSelection`):
- Implemented/available: `s`, `S`, `a`, `x`, `r`
- Placeholder/TODO: `p` (pause), `R` (resume)
- Missing from TS set: yolo start (`!`) path

## 5) Git/PR Actions

TS documented set includes `u,f,P,O,m,M,b,d,T,D` (and specific semantics).

Go current state (from `ActionMenu` + `handleSelection`):
- Implemented/available: `u`, `m`, `P`, `f`, `b`, `w` (cleanup worktree), `W` (delete+cleanup)
- Not matched to TS key/behavior set:
  - `O` (open PR) not present
  - `M` (abort merge) not present
  - `T` tombstone not present
  - TS cleanup keys `d`/`D` map differently in Go (`d` is delete task in task actions; cleanup is `w/W`)

## 6) Select/Bulk Operations

TS docs describe select mode with `a/5` toggle, `A` column select-all, `%` all, then `Space` for bulk action flow.

Go select mode (`handleSelectMode`) supports:
- `a` or `Space` toggle current
- `A` select current column
- `%` select all visible
- `*` invert selection (extra vs TS docs)
- `x` clear selection
- `Enter` opens bulk action menu

Bulk action menu in Go includes:
- move left/right
- set explicit status open/in_progress/blocked/done
- delete selected
- clear selection

Net:
- Core bulk workflows exist; initiation and some key choices differ from TS documentation.

## 7) Settings Surface

TS settings (docs) include many runtime config toggles (CLI tool, permissions, git, PR, notifications, network, linear sync, pattern matching).

Go settings overlay (`settings.go`) currently exposes mainly:
- show dependency phases
- auto-refresh
- compact card view
- theme choice
- actions: open config in editor, manage projects

Net gap:
- Most TS configuration controls are not exposed in Go UI yet.

## 8) Help Overlay Accuracy / Drift

Go help overlay (`help.go`) has drift vs actual behavior:
- It claims selection `A` = clear selection, while implementation uses `x` for clear and `A` for column select-all.
- It lists `Ctrl+L` refresh, but board key handler currently has no explicit `ctrl+l` case.

This is a documentation/UX inconsistency within Go implementation.

## Keybinding Delta (High-Signal)

## Matches
- Movement: `h/j/k/l`, arrows
- Modes: `g`, `v`, `/`, `f`, `,`, `?`
- Goto: `gg/ge/gh/gl/gw`
- View toggle: `Tab`

## TS-documented keys with no current Go equivalent
- `Space+!` (start yolo)
- `Space+H` (open Helix)
- `Space+M` (abort merge)
- `Space+O` (open PR)
- `Space+T` (tombstone)
- Some TS detail-panel attachment management flows as documented are not yet exposed with identical key paths in Go.

## Different semantics / remaps in Go
- `Space` opens task workspace overlay instead of pure prefix mode.
- Cleanup uses `w/W` in Go action menu rather than TS `d/D` patterns.
- Bulk actions are entered with `Enter` in select mode (Go) vs TS-documented `Space` flow.

## Recommended Follow-Up Work

1. Decide parity target for interaction model:
- Preserve Go Task Workspace UX and document it as intentional divergence, or
- Align Go with TS prefix action model for strict key-level parity.

2. Close high-impact keybind gaps first:
- `!` start yolo, `M` abort merge, `O` open PR, `T` tombstone.

3. Resolve Go internal help drift:
- Sync `help.go` key docs with `model.go` behavior (`A`/`x`, `Ctrl+l`).

4. Expand Go settings overlay toward TS configuration coverage.

5. If strict command parity is required, add missing Go CLI command groups or document a phased CLI parity contract.

## Evidence Pointers

- TS keybindings and feature docs:
  - `ts-opentui/docs/keybindings.md`
  - `ts-opentui/docs/README.md`
- TS CLI surface:
  - `az --help`
- Go board/action handlers:
  - `go-bubbletea/internal/app/model.go` (normal/goto/select/action + selection handling)
- Go overlays:
  - `go-bubbletea/internal/ui/overlay/action.go`
  - `go-bubbletea/internal/ui/overlay/task_workspace.go`
  - `go-bubbletea/internal/ui/overlay/settings.go`
  - `go-bubbletea/internal/ui/overlay/help.go`
- Go CLI surface:
  - `go run ./cmd/az --help`
