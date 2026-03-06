# Azedarach Go/Bubbletea Rewrite Plan

> Alternative rewrite exploring Go + Bubbletea as TUI framework

## Quick Links

| Document | Description |
|----------|-------------|
| [Overview](docs/01-overview.md) | Why Go/Bubbletea, trade-offs, performance targets |
| [Architecture](docs/02-architecture.md) | TEA pattern, Gleam→Go mapping, code examples |
| [Project Structure](docs/03-project-structure.md) | Directory layout, library choices, configuration |
| [Go Best Practices](docs/04-go-best-practices.md) | DI, context, errors, concurrency, testing |
| [Bubbletea Patterns](docs/05-bubbletea-patterns.md) | Nested models, commands, navigation, performance |
| [Implementation Phases](docs/06-implementation-phases.md) | Phase index with progress tracking |
| [Feature Matrix](docs/07-feature-matrix.md) | TypeScript→Go parity tracking (~100 features) |
| [Technical Deep Dive](docs/08-technical-deep-dive.md) | Challenges, solutions, testing, migration |

## Implementation Phases

| Phase | Focus | Status | Document |
|-------|-------|--------|----------|
| **1** | Core Framework | 🔲 | [phases/phase-1-core.md](docs/phases/phase-1-core.md) |
| **2** | Tracker Integration | 🔲 | [phases/phase-2-beads.md](docs/phases/phase-2-beads.md) |
| **3** | Overlays & Filters | 🔲 | [phases/phase-3-overlays.md](docs/phases/phase-3-overlays.md) |
| **4** | Session Management | 🔲 | [phases/phase-4-sessions.md](docs/phases/phase-4-sessions.md) |
| **5** | Git Operations | 🔲 | [phases/phase-5-git.md](docs/phases/phase-5-git.md) |
| **6** | Advanced Features | 🔲 | [phases/phase-6-advanced.md](docs/phases/phase-6-advanced.md) |

**Legend**: 🔲 Not Started | 🟡 In Progress | ✅ Complete

```
Phase 1 (Core)       →  Phase 2 (Tracker)    →  Phase 3 (Overlays)
     ↓                                                ↓
Phase 4 (Sessions)  ←──────────────────────→  Phase 5 (Git)
                              ↓
                    Phase 6 (Advanced Features)
```

## Executive Summary

This rewrite explores Go + [Bubbletea](https://github.com/charmbracelet/bubbletea) as an alternative to the Gleam/Shore rewrite. Both use The Elm Architecture (TEA), making the conceptual port straightforward.

### Why Go?

| Aspect | Benefit |
|--------|---------|
| **Single Binary** | No runtime dependencies |
| **Cross-Platform** | Native Windows support |
| **Performance** | Fast startup (~50ms), low memory |
| **Ecosystem** | Charmbracelet is production-tested (9,300+ projects) |
| **Distribution** | `go install`, Homebrew, goreleaser |

### Trade-offs vs Gleam

| Aspect | Gleam/OTP | Go/Bubbletea |
|--------|-----------|--------------|
| Concurrency | OTP actors (preemptive) | Goroutines (cooperative) |
| Fault Tolerance | Supervision trees | Manual error handling |
| Type System | Strong, functional | Strong, structural |
| Pattern Matching | Native | Type switches |

## Feature Parity Status

**Total Features: ~100**
- ✅ Planned: ~35 (35%)
- ⚠️ To be added: ~65 (65%)

### Critical for v1.0
1. Select mode & bulk operations
2. Goto mode with jump labels
3. Compact view toggle
4. Start+work & yolo modes
5. Delete/cleanup workflow
6. Diff viewer
7. Manual/Claude create & edit
8. Port allocation for dev servers
9. Confirm dialogs
10. Move tasks left/right

See [Feature Matrix](docs/07-feature-matrix.md) for complete tracking.

## Getting Started

```bash
# Build
go build -o bin/az ./cmd/az

# Run
./bin/az

# Development
go run ./cmd/az
```

## Key Patterns

### Nested Models (from Glow)

```go
type Model struct {
    common   *CommonModel     // Shared state
    board    *board.Model     // Sub-model
    overlays *overlay.Stack   // Overlay stack
    state    State            // Router
}
```

### Async Commands

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    case refreshMsg:
        return m, loadIssuesCmd  // Non-blocking
    case issuesLoadedMsg:
        m.tasks = msg.tasks
        return m, nil
}
```

### Dependency Injection

```go
type Client struct {
    runner CommandRunner  // Interface for testing
    logger *slog.Logger
}

func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
    return &Client{runner: runner, logger: logger}
}
```

See [Bubbletea Patterns](docs/05-bubbletea-patterns.md) and [Go Best Practices](docs/04-go-best-practices.md) for more.

## Related Files

```
go-bubbletea/
├── PLAN.md                     # This file (index)
├── ARCHITECTURE.md             # System diagrams
├── QUICK_REFERENCE.md          # Bubbletea cheat sheet
├── docs/
│   ├── 01-overview.md
│   ├── 02-architecture.md
│   ├── 03-project-structure.md
│   ├── 04-go-best-practices.md
│   ├── 05-bubbletea-patterns.md
│   ├── 06-implementation-phases.md  # Phase index
│   ├── 07-feature-matrix.md
│   ├── 08-technical-deep-dive.md
│   └── phases/                 # Individual phase plans
│       ├── phase-1-core.md
│       ├── phase-2-beads.md
│       ├── phase-3-overlays.md
│       ├── phase-4-sessions.md
│       ├── phase-5-git.md
│       └── phase-6-advanced.md
├── cmd/azedarach/              # Entry point stub
├── internal/                   # Implementation (to be built)
├── go.mod
└── go.sum
```

## Next Steps

1. Review this plan and decide if Go rewrite should proceed
2. If yes, begin Phase 1 implementation
3. Maintain both Gleam and Go rewrites in parallel for comparison
