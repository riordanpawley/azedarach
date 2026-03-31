# Azedarach

Terminal Kanban orchestration for parallel AI coding sessions, implemented in Go with Bubbletea.

## Overview

Azedarach is a terminal board and CLI for issue-driven development across parallel git worktrees.

It combines:
- A Bubbletea TUI (`az`) for daily workflow
- A daemon runtime (`azd`) for state authority and coordination
- Issue tracker commands (`az issue ...`)
- Agent session orchestration (`az session ...`, hooks, gate/dev flows)
- Project and operation management for multi-repo workflows

## Current Implementation

This repository ships the Go implementation as the canonical runtime:
- CLI: `cmd/az`
- Daemon: `cmd/azd`
- Core docs: `docs/`

## Quick Start

Build, link, and run:

```bash
just build-link-run
```

Build and link without starting interactive TUI:

```bash
just build-link-run -- --no-run
```

Then verify:

```bash
az --help
azd --help
```

## Feature Areas

### CLI + TUI

- Start interactive board: `az`
- Session lifecycle commands: `az session start|attach|kill|status`
- Project registry commands: `az project list|add|remove|switch`
- Snapshot and support commands: `az export`, `az sync`, `az prime`

### Agent Session Orchestration

- Starts work in isolated git worktrees tied to issues
- Supports attach/detach flow for active sessions
- Handles hook notifications: `az notify <event> <issue-id>`
- Installs hook configuration: `az hooks install <issue-id>`
- Runs quality gates: `az gate <issue-id>` and `az dev gate <issue-id>`
- Manages per-issue dev servers: `az dev start|stop|restart|status` and `az dev list`

### Issue Tracker Workflows

- CRUD and query: `az issue list|get|get-many|create|update|close|delete`
- Dependency graph operations: `az issue dep add|remove|bulk apply`
- Bulk planning flows: `az issue bulk-create`, `az issue bulk-update`
- Fanout workflow support: `az issue fanout`, `az issue fanout ready`, `az issue fanout drift`
- Integrity checks: `az issue doctor`

### Spec + Policy Integration

- Project-level spec gate toggle:
  - `az config set spec.enabled true`
  - `az config set spec.enabled false`
- Spec and implementation planning references live under `docs/`

### Daemon + Operations

- Daemon lifecycle recovery: `az daemon restart`
- Background operation control: `az operation list|get|cancel`
- Mailbox event flow for orchestrator/worker coordination: `az mail send|list|watch`

## Architecture

```mermaid
flowchart LR
  subgraph Frontend["Frontend (thin client)"]
    U[User]
    AZ[az CLI + Bubbletea TUI]
  end

  subgraph Boundary["Typed Contracts + IPC"]
    C[internal/contracts/*]
    IPC[internal/ipc/*]
    CLIENT[internal/client/*]
  end

  subgraph Daemon["Daemon (authoritative writer)"]
    AZD[azd daemon]
    STATE[Session/Worktree/Devserver state]
    OPS[Operation queue + revisioned events]
  end

  subgraph Adapters["Service adapters"]
    ISSUES[Issue tracker]
    GIT[git/worktree]
    TMUX[tmux sessions]
    DEV[devserver manager]
    PR[GitHub PR workflow]
  end

  U --> AZ
  AZ --> CLIENT
  CLIENT --> C
  CLIENT --> IPC
  IPC --> AZD
  AZD --> STATE
  AZD --> OPS
  AZD --> ISSUES
  AZD --> GIT
  AZD --> TMUX
  AZD --> DEV
  AZD --> PR
  OPS --> CLIENT
  CLIENT --> AZ
```

Authority model:
- `az`/TUI builds intents and renders projection state.
- `azd` owns lifecycle mutations (sessions/worktrees/devservers) and publishes revisioned updates.
- Cross-process payloads are typed contracts, not UI message types.

## Development Commands

```bash
just build         # build bin/az + bin/azd
just run           # restart daemon + run az
just test          # go test -v ./...
just type-check    # go build ./...
just boundary-check
```

Direct Go entrypoint examples:

```bash
go run ./cmd/az
go run ./cmd/azd
```

## System Requirements

- Go >= 1.21
- Git >= 2.20
- tmux >= 3.0
- GitHub CLI (`gh`) authenticated
- Issue tracker CLI configured (`az issue ...`)

## Documentation

- Overview: `docs/01-overview.md`
- Architecture: `docs/02-architecture.md`
- Project structure: `docs/03-project-structure.md`
- Feature matrix: `docs/07-feature-matrix.md`
- Recovery playbook: `docs/13-recovery-playbook.md`
- Release + Homebrew: `docs/15-go-release-and-homebrew.md`

## Release

Homebrew release flow:

```bash
just release-homebrew -- --patch --tap-dir ~/prog/homebrew-azedarach
```
