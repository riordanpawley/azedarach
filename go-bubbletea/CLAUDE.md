<!--
File: CLAUDE.md
Version: 1.0.0
Updated: 2025-12-21
Purpose: Claude Code entry point for Go/Bubbletea Azedarach development
-->

<ai_context version="1.0" tool="claude">

# Azedarach Project Context - Go/Bubbletea Implementation

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

## Critical Rules (Always Apply)

1. **Go Best Practices**: Follow [Standard Go Project Layout](https://github.com/golang-standards/project-layout):
   - `cmd/` - Main applications (minimal wiring only)
   - `internal/` - Private code (compiler-enforced encapsulation)
   - `pkg/` - Public libraries (reusable by others, use sparingly)
   - `testdata/` - Test fixtures

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Beads Tracker**: ALWAYS use `bd` CLI commands for beads operations. Use `bd search` for discovery, `bd ready` for unblocked work. NEVER use `bd list` (causes context bloat).

4. **Branch Workflow**: Azedarach pushes branches at worktree creation (`git push -u`) so they have upstreams and use normal `bd sync`. If you're on a truly ephemeral branch (no upstream), DON'T run `bd sync --from-main` at session end - it overwrites local beads changes.

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

# Beads (Task Management)
bd search "keywords"              # Search issues (PRIMARY - not list!)
bd ready                          # Find unblocked work
bd create --title="..." --type=task  # Create issue
bd update <id> --status=in_progress  # Update status
bd close <id>                     # Mark complete
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
│   ├── services/     # Business logic (Beads, Tmux, Git)
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
│  • Beads Client (bd CLI wrapper)                                        │
│  • Tmux Client (session management)                                      │
│  • Git Client (worktree operations)                                      │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Task Management

**Track ALL work in beads** (preserves context across sessions):

```bash
bd ready                          # Find available work
bd update <id> --status=in_progress  # Claim it
bd close <id>                     # Mark complete
```

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-beads** - Beads integration (bd prime, /bd-* commands)
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>
