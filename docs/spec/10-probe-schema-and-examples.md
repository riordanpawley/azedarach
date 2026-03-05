# 10 - Probe Schema and Examples

This section defines a canonical machine-readable probe payload contract for deterministic automation.

The probe contract is behavioral and transport-agnostic.

## 10.1 Probe Contract Goals

- provide side-effect-free state introspection
- allow stable assertions for mode, focus, visible entities, and operation status
- enable ordering-safe snapshot comparisons across a workflow

## 10.2 Versioning Rules

- Every payload MUST include `schemaVersion`.
- Backward-compatible additions are allowed within a major version.
- Breaking field shape changes require major version increment.

## 10.3 Canonical Payload Shape (v1)

```json
{
  "schemaVersion": "1.0",
  "snapshot": {
    "revision": 1842,
    "capturedAt": "2026-03-03T02:30:11Z"
  },
  "app": {
    "projectId": "azedarach",
    "projectPath": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db",
    "view": "kanban",
    "mode": "NOR",
    "overlays": []
  },
  "focus": {
    "issueId": "kqd",
    "column": "in_progress",
    "indexInColumn": 5
  },
  "board": {
    "visibleWindow": {
      "columns": ["open", "in_progress", "blocked", "closed"],
      "issueIds": ["kpn", "kqr", "kqd", "kqt", "kqx"]
    },
    "indicators": {
      "kqd": {
        "session": "busy",
        "git": "dirty",
        "pr": "open",
        "dependencySummary": {
          "up": 2,
          "down": 1,
          "blocked": 0
        },
        "staleness": "fresh"
      }
    },
    "loading": {
      "visibleWindowHydrated": true,
      "offscreenHydrationPending": true
    }
  },
  "operations": {
    "queue": [
      {
        "operationId": "op-7fc1",
        "kind": "create-pr",
        "state": "running",
        "step": "push-branch",
        "cancelable": false
      }
    ]
  },
  "sync": {
    "linear": {
      "enabled": true,
      "rateLimit": {
        "windowSeconds": 60,
        "maxRequestsPerWindow": 30,
        "burstCapacity": 10,
        "usedInWindow": 10,
        "queuedOutbound": 6,
        "nextDispatchAt": "2026-03-03T02:30:20Z"
      }
    }
  },
  "agentBootstrap": {
    "issueLookupCommand": "az show kqd",
    "backendSpecificCommandLeakDetected": false
  },
  "commands": {
    "az": {
      "lastOperation": "update",
      "lastCommand": "az update kqd --design \"...\"",
      "lastError": null
    }
  },
  "errors": {
    "recent": []
  }
}
```

## 10.4 Field Semantics

- `snapshot.revision`: monotonically increasing value for ordering assertions.
- `app.mode`: active primary mode (`NOR`, `ACT`, `GTO`, `SEL`, `SRC`, `FLT`, `SRT`) or overlay-focused mode.
- `app.projectPath`: active project root path for current board context.
- `app.canonicalDbPath`: active project canonical SQLite path (for example `<project-root>/.azedarach/azedarach.db`).
- `app.overlays`: currently active overlays in front-to-back order.
- `board.visibleWindow.issueIds`: IDs rendered in current viewport.
- `board.indicators[*].staleness`: freshness hint (`fresh`, `loading`, `stale`) for user-visible indicator convergence checks.
- `operations.queue[*].state`: one of `queued`, `running`, `succeeded`, `failed`, `canceled`.
- `sync.linear.rateLimit`: outbound throttling snapshot; present when Linear sync target is enabled.
- `agentBootstrap.issueLookupCommand`: active session bootstrap issue lookup command (must use `az show <issue-id>` when bootstrap guidance is present).
- `agentBootstrap.backendSpecificCommandLeakDetected`: diagnostic boolean for backend-specific command leakage in bootstrap guidance.
- `commands.az.lastOperation`: last observed top-level `az` operation kind (for example `show`, `create`, `q`, `update`, `close`, `reopen`, `delete`, `list`, `ready`, `blocked`, `search`, `stale`, `count`, `dep.*`, `config.*`, `stats`) when available.

## 10.5 Example: Overlay + Selection State

```json
{
  "schemaVersion": "1.0",
  "snapshot": {
    "revision": 1888,
    "capturedAt": "2026-03-03T02:31:08Z"
  },
  "app": {
    "projectId": "azedarach",
    "projectPath": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db",
    "view": "kanban",
    "mode": "SEL",
    "overlays": ["detail"]
  },
  "focus": {
    "issueId": "kqt",
    "column": "in_progress",
    "indexInColumn": 6
  },
  "board": {
    "visibleWindow": {
      "columns": ["open", "in_progress", "blocked", "closed"],
      "issueIds": ["kqr", "kqd", "kqt", "kqx", "kra"]
    },
    "selection": {
      "selectedIssueIds": ["kqd", "kqt"],
      "count": 2
    }
  },
  "operations": {
    "queue": []
  },
  "agentBootstrap": {
    "issueLookupCommand": "az show kqt",
    "backendSpecificCommandLeakDetected": false
  },
  "commands": {
    "az": {
      "lastOperation": "show",
      "lastCommand": "az show kqt --json",
      "lastError": null
    }
  },
  "errors": {
    "recent": [
      {
        "operationId": "op-7fb9",
        "kind": "merge",
        "message": "Conflict in src/ui/board.ts",
        "at": "2026-03-03T02:30:52Z"
      }
    ]
  }
}
```

## 10.6 Assertion Guidance

- Assert both state and ordering: compare `snapshot.revision` and `capturedAt`.
- For responsiveness checks, assert viewport IDs update before off-screen hydration completes.
- For high-risk flows, pair probe assertions with visual snapshots from Section 06.
