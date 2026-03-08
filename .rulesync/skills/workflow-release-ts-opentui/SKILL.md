---
name: workflow-release-ts-opentui
description: "ts-opentui Release Workflow Skill"
targets: ["claudecode"]
---

# ts-opentui Release Workflow Skill

**Version:** 1.0
**Purpose:** Standardize patch/minor/major release flow for `ts-opentui` while honoring repository git policy.

## When To Use

Use this skill when the task is "release next version" or "bump version" for `ts-opentui`.

## Core Rules

- Prefer release automation entrypoints:
  - `just release-ts-opentui <patch|minor|major>` for release-only
  - `just release-ts-opentui-homebrew <patch|minor|major>` for release + tap formula update
    (default tap path: `/Users/riordan/prog/homebrew-azedarach`, override with `AZ_HOMEBREW_TAP_DIR`)
  - The one-step command waits for release assets (`SHA256SUMS.txt`) before generating and publishing the tap formula.
- The release script performs pull + push. Only run the full script when remote sync is explicitly requested.
- If remote sync is not explicitly requested, do local-only release prep (version bump, checks, commit, local tag).
- Always include the issue ID in commit messages.
- Keep `az issue` notes/status current during release work.

## Commands

### 1) Session + context

```bash
az prime
az issue get <issue-id>
git status --short --branch
git log --oneline -5
```

### 2) Full publish release (remote sync explicitly requested)

```bash
git checkout main
git status --short --branch
just release-ts-opentui-homebrew patch

# Optional override:
AZ_HOMEBREW_TAP_DIR=/path/to/homebrew-azedarach just release-ts-opentui-homebrew patch
```

Use `minor` or `major` as needed.

### 3) Local-only release prep (default when remote sync is not requested)

```bash
# Read current version
rg -n '"version"' ts-opentui/package.json

# Bump version in ts-opentui/package.json
# Run quality gate
cd ts-opentui && bun run type-check

# Commit + local tag from repo root
git add ts-opentui/package.json
git commit -m "<issue-id>: release vX.Y.Z (patch)"
git tag -a "vX.Y.Z" -m "Release vX.Y.Z"
```

## Completion Checklist

- `git status` is clean
- version updated in `ts-opentui/package.json`
- quality gate passes (`bun run type-check`)
- commit created with issue ID
- local tag created (and pushed only if explicitly requested)
- `az issue` notes updated with validation + spec impact

## Spec Impact Guidance

For pure version metadata changes only:

`Spec impact: none; metadata-only change in ts-opentui/package.json (no behavior change).`
