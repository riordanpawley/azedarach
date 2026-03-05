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

## 12.6 Tombstone Visibility Semantics

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

## 12.7 Out-of-Scope Restore Semantics (Current Contract)

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
