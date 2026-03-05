# 12 - Az CLI JSON Schema and Examples

This section defines the canonical JSON payload contract for top-level `az` commands.

The schema applies when command execution requests machine-readable output (for example `--json`).

## 12.1 Goals

- provide deterministic command payloads for automation and agent workflows
- standardize success and failure envelopes across command families
- preserve project-context traceability in every payload

## 12.2 Versioning Rules

- Every JSON payload MUST include `schemaVersion`.
- Backward-compatible field additions are allowed within a major version.
- Breaking shape changes require major version increment.

## 12.3 Canonical Envelope (v1)

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {},
  "error": null,
  "meta": {
    "durationMs": 18,
    "at": "2026-03-05T03:20:11Z"
  }
}
```

Field requirements:

- `command`: canonical operation name (for example `show`, `project.add`, `dep.tree`).
- `commandPath`: resolved CLI path tokens for executed command.
- `project`: active project context used for command execution.
- `ok`: command outcome boolean.
- `result`: non-null only when `ok=true`.
- `error`: non-null only when `ok=false`.
- `meta`: deterministic execution metadata for debugging/automation.

## 12.4 Error Object Contract

```json
{
  "code": "issue_not_found",
  "message": "Issue kqd was not found in active project",
  "remediation": "Run az list --json to inspect available issues",
  "details": {
    "issueId": "kqd"
  }
}
```

Error requirements:

- `code`: stable machine-parseable identifier.
- `message`: concise human-readable failure summary.
- `remediation`: single actionable next step.
- `details`: optional structured context relevant to error type.

## 12.5 Success Example: Project Switch

```json
{
  "schemaVersion": "1.0",
  "command": "project.switch",
  "commandPath": ["az", "project", "switch"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "activeProject": "azedarach",
    "persistedDefault": "azedarach",
    "scope": "persistent"
  },
  "error": null,
  "meta": {
    "durationMs": 24,
    "at": "2026-03-05T03:21:08Z"
  }
}
```

## 12.6 Success Example: Create With Parent Linkage

```json
{
  "schemaVersion": "1.0",
  "command": "create",
  "commandPath": ["az", "create"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "issue": {
      "id": "kqf",
      "title": "Implement cache invalidation",
      "status": "open"
    },
    "relationships": {
      "parentIssueId": "kqd",
      "edgeType": "parent-child"
    }
  },
  "error": null,
  "meta": {
    "durationMs": 21,
    "at": "2026-03-05T03:21:43Z"
  }
}
```

Create-with-parent requirements:

- `--parent <issue-id>` MUST fail deterministically when parent does not exist in active project context.
- failed parent validation MUST NOT leave orphan child issue rows in canonical storage.

## 12.7 Show Dependency Projection Contract

Projection parameter contract:

- `--deps`: `none|counts|direct|verbose` (default `counts`)
- `--dep-depth`: integer `>= 0` in all projection modes (`0` = counts-only, no expansion)
- `--dep-type`: optional relation-type filter (csv) for `direct|verbose`
- `--dep-limit`, `--dep-node-limit`: deterministic truncation controls for projection payloads

### 12.7.1 Success Example: `az show` default typed counts

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "issue": {
      "id": "kqd",
      "title": "Refactor sync queue",
      "status": "in_progress"
    },
    "dependencyCountsByType": {
      "blocking": 8,
      "blocked-by": 1,
      "parent-child": 2
    }
  },
  "error": null,
  "meta": {
    "durationMs": 12,
    "at": "2026-03-05T03:22:10Z"
  }
}
```

### 12.7.2 Success Example: `az show --deps=direct`

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "issue": {
      "id": "kqd",
      "title": "Refactor sync queue"
    },
    "dependencyProjection": {
      "mode": "direct",
      "depth": 1,
      "order": "relationType,issueId",
      "byType": {
        "blocked-by": [{ "issueId": "kqa", "title": "Stabilize storage adapter" }],
        "blocking": [{ "issueId": "kqf", "title": "Add cache key namespacing" }]
      },
      "truncated": false
    }
  },
  "error": null,
  "meta": {
    "durationMs": 17,
    "at": "2026-03-05T03:22:22Z"
  }
}
```

### 12.7.3 Success Example: `az show --deps=verbose --dep-depth=0`

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "issue": {
      "id": "kqd",
      "title": "Refactor sync queue"
    },
    "dependencyCountsByType": {
      "blocking": 8,
      "blocked-by": 1
    },
    "dependencyProjection": {
      "mode": "verbose",
      "depth": 0,
      "byType": {},
      "truncated": false
    }
  },
  "error": null,
  "meta": {
    "durationMs": 14,
    "at": "2026-03-05T03:22:34Z"
  }
}
```

### 12.7.4 Failure Example: invalid dependency projection arguments

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": false,
  "result": null,
  "error": {
    "code": "invalid_argument",
    "message": "--dep-depth must be >= 0",
    "remediation": "Run az show <issue-id> --deps=counts --dep-depth 0",
    "details": {
      "argument": "dep-depth",
      "value": "-1"
    }
  },
  "meta": {
    "durationMs": 3,
    "at": "2026-03-05T03:22:47Z"
  }
}
```

### 12.7.5 Failure Example: mutation-time cycle rejection

```json
{
  "schemaVersion": "1.0",
  "command": "dep.add",
  "commandPath": ["az", "dep", "add"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": false,
  "result": null,
  "error": {
    "code": "cycle_rejected",
    "message": "Dependency edge would introduce disallowed cycle",
    "remediation": "Run az dep cycles --json and choose a non-cyclic relation target",
    "details": {
      "sourceIssueId": "kqd",
      "targetIssueId": "kqa"
    }
  },
  "meta": {
    "durationMs": 8,
    "at": "2026-03-05T03:23:01Z"
  }
}
```

## 12.8 Tombstone Visibility Semantics

Default behavior:

- tombstoned issues are excluded from `az list/ready/blocked/search/stale/count`
- `az show <issue-id>` without include-deleted mode returns tombstone-aware error

Include-deleted behavior:

- include-deleted mode returns tombstoned records with `deleted=true` and `tombstone` metadata

```json
{
  "schemaVersion": "1.0",
  "command": "show",
  "commandPath": ["az", "show"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": true,
  "result": {
    "issue": {
      "id": "kqd",
      "title": "Refactor sync queue",
      "deleted": true,
      "tombstone": {
        "deletedAt": "2026-03-05T03:22:41Z",
        "reason": "superseded",
        "replacementIssueId": "kra"
      }
    }
  },
  "error": null,
  "meta": {
    "durationMs": 19,
    "at": "2026-03-05T03:22:42Z"
  }
}
```

## 12.9 Out-of-Scope Restore Semantics (Current Contract)

The current command contract does not define a restore operation for tombstoned issues.

Restore attempts MUST return deterministic unsupported-operation errors:

```json
{
  "schemaVersion": "1.0",
  "command": "restore",
  "commandPath": ["az", "restore"],
  "project": {
    "id": "azedarach",
    "path": "/Users/dev/prog/azedarach",
    "canonicalDbPath": "/Users/dev/prog/azedarach/.azedarach/azedarach.db"
  },
  "ok": false,
  "result": null,
  "error": {
    "code": "unsupported_operation",
    "message": "Issue restore is not supported in current contract",
    "remediation": "Create a new issue and link it to the tombstoned issue ID",
    "details": {
      "operation": "restore"
    }
  },
  "meta": {
    "durationMs": 5,
    "at": "2026-03-05T03:23:10Z"
  }
}
```
