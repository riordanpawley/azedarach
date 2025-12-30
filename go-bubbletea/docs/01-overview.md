# Overview: Go/Bubbletea Rewrite

> Alternative rewrite exploring Go + Bubbletea as TUI framework

## Executive Summary

This document outlines a potential rewrite of Azedarach in Go using the [Bubbletea](https://github.com/charmbracelet/bubbletea) TUI framework. Like the existing Gleam rewrite, Bubbletea uses The Elm Architecture (TEA), making the conceptual port straightforward.

## Why Go/Bubbletea?

### Advantages

| Aspect | Benefit |
|--------|---------|
| **Single Binary** | Go compiles to a single static binary - no runtime dependencies |
| **Cross-Platform** | Native Windows support (unlike BEAM/Erlang) |
| **Performance** | Fast startup, low memory footprint |
| **Ecosystem** | Charmbracelet ecosystem is mature & production-tested (9,300+ projects use Bubbletea) |
| **Distribution** | Easy to distribute via `go install`, Homebrew, etc. |
| **Same Architecture** | TEA model matches Gleam/Shore - 1:1 conceptual mapping |
| **Familiar** | Go is widely known; easier to find contributors |

### Trade-offs vs Gleam

| Aspect | Gleam/OTP | Go/Bubbletea |
|--------|-----------|--------------|
| Concurrency Model | OTP actors (preemptive) | Goroutines (cooperative) |
| Fault Tolerance | Supervision trees | Manual error handling |
| Hot Code Reload | Erlang VM supports it | Not available |
| Type System | Strong, functional | Strong, structural |
| Pattern Matching | Native | Switch statements |
| Immutability | Default | Manual discipline |

### When Go Makes Sense

1. **Distribution priority** - Need easy installation across platforms
2. **Team familiarity** - Go more common than Gleam
3. **Windows users** - Erlang setup on Windows is painful
4. **Binary size** - Go binaries smaller than BEAM releases

## Comparison: All Three Implementations

| Aspect | TypeScript | Gleam | Go |
|--------|------------|-------|-----|
| **Lines of Code** | ~33,000 | ~16,500 | ~8,000 (est.) |
| **Architecture** | Effect services | OTP actors | Goroutines |
| **UI Framework** | React + OpenTUI | Shore (TEA) | Bubbletea (TEA) |
| **State** | SubscriptionRef + Atoms | TEA Model | TEA Model |
| **Concurrency** | Effect fibers | OTP processes | Goroutines |
| **Binary Size** | N/A (Node.js) | ~30MB (BEAM) | ~10MB |
| **Startup Time** | ~500ms | ~200ms | ~50ms |
| **Windows** | Yes (Node) | Difficult | Yes (native) |
| **Distribution** | npm | escript/release | go install |

## Performance Targets

| Metric | Target | Notes |
|--------|--------|-------|
| Startup time | < 100ms | Cold start to first render |
| Binary size | < 15MB | Single static binary |
| Memory usage | < 50MB | With 100+ tasks loaded |
| Refresh rate | 60 FPS | Smooth scrolling |
| State detection | < 500ms | From Claude output to UI update |
| Beads refresh | < 200ms | `bd list` round trip |

## Next Steps

1. Review this plan and decide if Go rewrite should proceed
2. If yes, create initial project skeleton
3. Begin Phase 1 implementation
4. Maintain both Gleam and Go rewrites in parallel for comparison

## Resources

- [Bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) - TUI components
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) - Terminal styling
- [Charm tutorials](https://charm.sh/blog/) - Framework guides
- [gogh-themes](https://github.com/willyv3/gogh-themes/lipgloss) - Theme colors
