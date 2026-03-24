# go-bubbletea Event Catalog

Concrete event names and required fields for `go-bubbletea/` services.

## Global Fields (all events)

- `event`
- `service` (for this repo: `azedarach-go-bubbletea`)
- `operation` (method/use-case name)
- `request_id` (if present in call path)
- `trace_id`, `span_id` (when OTel context is available)
- `duration_ms` (for completed operations)
- `outcome` (`success` or `error`)

## CLI (`internal/cli/commands.go`)

- `cli.session.start.requested`
- `cli.session.start.completed`
- `cli.session.start.failed`
- `cli.session.attach.requested`
- `cli.session.attach.completed`
- `cli.session.attach.failed`
- `cli.session.kill.requested`
- `cli.session.kill.completed`
- `cli.session.kill.failed`
- `cli.session.status.requested`
- `cli.session.status.completed`
- `cli.session.status.failed`

Required fields:

- `bead_id`
- `command` (`start`, `attach`, `kill`, `status`)

Optional fields:

- `base_branch`
- `session_exists`
- `active_session_count`

## Worktree Session Service (`internal/services/worktree/session.go`)

- `worktree.session.create.requested`
- `worktree.session.create.completed`
- `worktree.session.create.failed`
- `worktree.session.start.requested`
- `worktree.session.start.completed`
- `worktree.session.start.failed`
- `worktree.session.stop.requested`
- `worktree.session.stop.completed`
- `worktree.session.stop.failed`
- `worktree.session.delete.requested`
- `worktree.session.delete.completed`
- `worktree.session.delete.failed`
- `worktree.session.status.updated`

Required fields:

- `bead_id`
- `tmux_session`
- `branch`

Optional fields:

- `worktree_path`
- `yolo`
- `session_status`

## PR Workflow (`internal/services/pr/workflow.go`)

- `pr.create.requested`
- `pr.create.completed`
- `pr.create.failed`
- `pr.get.requested`
- `pr.get.completed`
- `pr.get.failed`
- `pr.list.requested`
- `pr.list.completed`
- `pr.list.failed`
- `pr.merge.requested`
- `pr.merge.completed`
- `pr.merge.failed`
- `pr.close.requested`
- `pr.close.completed`
- `pr.close.failed`
- `pr.ready.requested`
- `pr.ready.completed`
- `pr.ready.failed`

Required fields:

- `branch` (where relevant)
- `pr_number` (where relevant)

Optional fields:

- `base_branch`
- `draft`
- `strategy`
- `bead_id`

## Git/Tmux/Beads Client Boundaries

Use boundary events for all command runner calls:

- `dependency.git.command.completed|failed`
- `dependency.tmux.command.completed|failed`
- `dependency.beads.command.completed|failed`
- `dependency.gh.command.completed|failed`

Required fields:

- `dependency.name` (`git`, `tmux`, `beads`, `gh`)
- `dependency.operation` (high-level action, not full raw command)
- `exit_code` (on completion/failure)

Optional fields:

- `stderr_class` (sanitized)
- `retryable`

## Redaction Rules for This Repo

- Do not log raw command output (`stdout`, `stderr`) at `INFO`.
- Do not log full shell command strings when they may contain tokens.
- Do not log environment variable values.
- If command output is needed for debugging, log sanitized summaries at `DEBUG`.

## Migration Guidance for Existing Logs

Current code has message-style logs like `starting session` and `PR created`.
When touching these paths, migrate to event-first fields:

```go
logger.InfoContext(ctx, "session start completed",
    slog.String("event", "cli.session.start.completed"),
    slog.String("operation", "StartCommand"),
    slog.String("bead_id", beadID),
    slog.String("outcome", "success"),
    slog.Duration("duration_ms", time.Since(start)),
)
```
