# go-bubbletea (rewrite)

Fresh Go/Bubble Tea rewrite for Azedarach, driven by `docs/spec/*` and bead plan `az-i38x`.

## Status

- Active rewrite (greenfield)
- Bubble Tea v2+ (`charm.land/bubbletea/v2`)
- TEA-first architecture (`Update` pure transitions, side effects in `Cmd`)

## Quick Start

```bash
make test
make build
./bin/az
```

## Current scaffold coverage

- App model + modal state machine baseline
- Domain contracts (modes, statuses, relations, operation states, sort/view modes)
- Operation orchestrator with idempotency + per-issue serialization
- Adapter contracts + typed error taxonomy
- Session, git/pr, and planning workflow scaffolding
- Deterministic testkit + versioned E2E probe schema
- Overlay stack, settings/projects/attachments services
- ADR docs and spec trace matrix template

## Notes

- This app is an internal tool and is optimized for deterministic behavior and fast AI-agent handoff.
- See `docs/spec-trace-matrix.md` for clause-to-bead-to-test mapping template.
