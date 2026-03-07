---
targets:
  - agentsmd
description: go-bubbletea scoped context
agentsmd:
  subprojectPath: go-bubbletea
---
<!--
File: CONTEXT.md
Version: 1.0.0
Updated: 2025-12-21
Purpose: Canonical go-bubbletea AI context source synced to nested entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Project Context - Go/Bubbletea Implementation

> TUI Kanban board for orchestrating parallel Claude Code sessions with issue tracking

## Critical Rules (Always Apply)

1. **Go Best Practices**: Follow [Standard Go Project Layout](https://github.com/golang-standards/project-layout):
   - `cmd/` - Main applications (minimal wiring only)
   - `internal/` - Private code (compiler-enforced encapsulation)
   - `pkg/` - Public libraries (reusable by others, use sparingly)
   - `testdata/` - Test fixtures

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Issue Tracking**: ALWAYS start with `az prime`, then use `az issue` for issue operations.

4. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested. When already in the target worktree, use plain `git` commands; use `git -C <path>` only when intentionally targeting a different path.

5. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

6. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

7. **🚨 CRITICAL: Commit Before Done 🚨**: Before saying "done", "complete", "finished", or stopping work, you MUST commit all changes. Uncommitted work is LOST work.

   **MANDATORY CHECKLIST** (run these commands):
   ```bash
   git status                    # Check for uncommitted changes
   git add -A                    # Stage all changes
   git commit -m "descriptive message"   # Commit with clear message
   ```

   **If work is complete:** Use a proper descriptive commit message
   **If work is partial/WIP:** Use `git commit -m "wip: brief description of state"`

   **This applies when you:**
   - Say "done", "complete", "finished", "all set", etc.
   - Are about to stop responding
   - Have completed a task or subtask
   - Are switching to a different task

8. **Dependency Injection via Interfaces**: Accept interfaces, return structs:
   ```go
   // GOOD: Accept interface
   type CommandRunner interface {
       Run(ctx context.Context, name string, args ...string) ([]byte, error)
   }

   func NewClient(runner CommandRunner) *Client { ... }
   ```
   This enables testing with mocks and loose coupling.

9. **Functional Options Pattern**: For complex constructors with optional configuration:
   ```go
   type Option func(*Model)

   func WithLogger(logger *slog.Logger) Option {
       return func(m *Model) { m.logger = logger }
   }

   func NewModel(opts ...Option) *Model {
       m := &Model{}
       for _, opt := range opts {
           opt(m)
       }
       return m
   }
   ```

10. **Bubbletea Model Architecture: Nested Models Pattern**:
    - Use nested models with a top-level router
    - Share common state via pointer (CommonModel) to avoid duplication
    - Pass ALL messages to relevant sub-models, not just "active" one
    - Route messages: global handlers → overlays → current view

11. **Bubbletea Init Pattern: Batch Sub-Model Initialization**:
    ```go
    func (m Model) Init() tea.Cmd {
        return tea.Batch(
            m.board.Init(),
            m.detail.Init(),
            m.settings.Init(),
            loadInitialData,  // Your custom init command
        )
    }
    ```

12. **Context Propagation**: Always pass `context.Context` as first argument to functions that do I/O or goroutine work:
    ```go
    func (c *Client) List(ctx context.Context) ([]domain.Task, error) { ... }
    ```

13. **Error Handling**: Use Go's idiomatic error handling:
    - Return errors from functions, never swallow them
    - Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`
    - Use `slog` for structured logging

14. **Testing**: Write tests alongside code (`*_test.go`):
    - Use table-driven tests for multiple cases
    - Mock external dependencies via interfaces
    - Keep tests fast and deterministic

15. **Goroutines and Channels**: Use patterns from `go-concurrency.skill.md`:
    - Prefer `context.Context` for cancellation
    - Use buffered channels when known capacity
    - Never send on closed channels (detect via select with default)

## Quick Commands

```bash
# Development
make build                       # Build the Go binary
make test                        # Run tests
make run                         # Build and run

# Search (modern tools)
rg "pattern" --type go           # Search content (NOT grep)
fd "filename" -t f              # Find files (NOT find)

# Issue Tracking
az prime                                                       # Session primer + workflow guidance
az issue --help                                                # Command reference
az issue get <issue-id>                                        # Show issue details
az issue create "Issue title" --description "Detailed context" # Create issue
az issue update <issue-id> --notes "progress update"           # Update progress
az issue close <issue-id>                                      # Mark complete
```

## Architecture Quick Reference

```
go-bubbletea/
├── cmd/              # Main applications (minimal wiring)
│   └── az/          # TUI entry point
├── internal/         # Private code (compiler-enforced)
│   ├── app/          # Bubbletea application logic
│   ├── cli/          # CLI argument parsing
│   ├── config/       # Configuration management
│   ├── core/         # Domain models and services
│   ├── services/     # Business logic (Linear, Tmux, Git)
│   ├── types/        # Type definitions
│   └── ui/          # Bubbletea UI components
├── docs/             # Documentation
├── Makefile          # Build commands
└── go.mod            # Go module definition
```

**Stack:** Go, Bubbletea (TUI framework), Lip Gloss (styling), Bubbles (components), tmux, git

## Key Technologies

- **Bubbletea**: Elm Architecture for terminal UI (Model-Update-View)
- **Lip Gloss**: Terminal styling (colors, borders, spacing)
- **Bubbles**: Pre-built UI components (lists, inputs, spinners)
- **tmux**: Terminal multiplexer for session management
- **slog**: Structured logging (Go 1.21+)

## Domain Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Bubbletea TUI (Model-Update-View)                 │
│  ┌─────────┐  ┌─────────────┐  ┌─────────┐  ┌────────┐  ┌────────┐  │
│  │  open   │ │ in_progress │ │ blocked │ │ review │ │ closed │  │
│  └─────────┘  └─────────────┘  └─────────┘  └────────┘  └────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Service Layer (Goroutines)                         │
│  • Session Monitor (polls tmux for state changes)                        │
│  • Issue tracker client (`az issue`)                                     │
│  • Tmux Client (session management)                                      │
│  • Git Client (worktree operations)                                      │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Task Management

**Track ALL work through issue tracking** (preserves context across sessions).

Run `az prime` first in each session, then use `az issue` for all issue operations.
Skills are task-scoped references, not mandatory bootstrap. Load only the specific skill(s) needed for the current task.

```bash
az issue --help
az issue get <issue-id>
az issue create "Issue title" --description "Detailed context"
az issue update <issue-id> --notes "progress update"
az issue close <issue-id>
```

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>
