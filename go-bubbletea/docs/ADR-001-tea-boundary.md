# ADR-001: Tea Boundary

## Status
Accepted

## Context
Bubble Tea update/view logic should stay deterministic and testable while side effects (IO, git, tmux, beads) remain replaceable.

## Decision
- Keep Bubble Tea models pure and message-driven.
- Move side effects behind internal service interfaces.
- Convert service outputs into typed messages before touching UI state.

## Consequences
- Faster unit tests and stable replay of message sequences.
- Clear seams for deterministic testkit clocks/IDs.
- More adapter code at the boundary, but lower coupling.
