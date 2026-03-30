# Overview: Go/Bubbletea Implementation

> Go + Bubbletea as the active TUI framework for Azedarach

## Executive Summary

This document outlines the Go implementation of Azedarach using the [Bubbletea](https://github.com/charmbracelet/bubbletea) TUI framework. Bubbletea uses The Elm Architecture (TEA), which keeps model/update/view boundaries explicit.

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

## Current Implementation Snapshot

| Aspect | Go |
|--------|-----|
| **Architecture** | Goroutines + daemon/client boundaries |
| **UI Framework** | Bubbletea (TEA) |
| **State** | TEA Model |
| **Distribution** | `go install` / Homebrew |

## Performance Targets

| Metric | Target | Notes |
|--------|--------|-------|
| Startup time | < 100ms | Cold start to first render |
| Binary size | < 15MB | Single static binary |
| Memory usage | < 50MB | With 100+ tasks loaded |
| Refresh rate | 60 FPS | Smooth scrolling |
| State detection | < 500ms | From Claude output to UI update |
| Linear refresh | < 200ms | `az issue list --output json --compact --all` round trip |

## Next Steps

1. Continue phase delivery against tracked issues
2. Keep boundary checks and regression guards green
3. Update docs as runtime behavior changes

## Resources

- [Bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) - TUI components
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) - Terminal styling
- [Charm tutorials](https://charm.sh/blog/) - Framework guides
- [gogh-themes](https://github.com/willyv3/gogh-themes/lipgloss) - Theme colors
