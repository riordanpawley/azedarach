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
    "view": "kanban",
    "mode": "NOR",
    "overlays": []
  },
  "focus": {
    "issueId": "az-142",
    "column": "in_progress",
    "indexInColumn": 5
  },
  "board": {
    "visibleWindow": {
      "columns": ["open", "in_progress", "blocked", "closed"],
      "issueIds": ["az-131", "az-139", "az-142", "az-144", "az-148"]
    },
    "indicators": {
      "az-142": {
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
  "errors": {
    "recent": []
  }
}
```

## 10.4 Field Semantics

- `snapshot.revision`: monotonically increasing value for ordering assertions.
- `app.mode`: active primary mode (`NOR`, `ACT`, `GTO`, `SEL`, `SRC`, `FLT`, `SRT`) or overlay-focused mode.
- `app.overlays`: currently active overlays in front-to-back order.
- `board.visibleWindow.issueIds`: IDs rendered in current viewport.
- `board.indicators[*].staleness`: freshness hint (`fresh`, `loading`, `stale`) for user-visible indicator convergence checks.
- `operations.queue[*].state`: one of `queued`, `running`, `succeeded`, `failed`, `canceled`.

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
    "view": "kanban",
    "mode": "SEL",
    "overlays": ["detail"]
  },
  "focus": {
    "issueId": "az-144",
    "column": "in_progress",
    "indexInColumn": 6
  },
  "board": {
    "visibleWindow": {
      "columns": ["open", "in_progress", "blocked", "closed"],
      "issueIds": ["az-139", "az-142", "az-144", "az-148", "az-151"]
    },
    "selection": {
      "selectedIssueIds": ["az-142", "az-144"],
      "count": 2
    }
  },
  "operations": {
    "queue": []
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
