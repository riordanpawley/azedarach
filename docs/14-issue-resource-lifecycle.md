# Issue Resource Lifecycle

Issue resource hooks are opt-in project configuration for local resources whose
lifetime is tied to an Azedarach issue session, not to a project app command.

Example configuration:

```json
{
  "issueResources": {
    "env": {
      "DATABASE_NAME": "chefy_$AZEDARACH_ISSUE_ID",
      "DATABASE_URL": "postgres://localhost/$DATABASE_NAME"
    },
    "prepareCommands": [
      "just local-services-up",
      "just db-create \"$DATABASE_NAME\"",
      "just db-migrate \"$DATABASE_NAME\""
    ],
    "failedStartCleanupCommands": [
      "just db-drop-if-empty \"$DATABASE_NAME\""
    ],
    "cleanupCommands": [
      "just db-drop \"$DATABASE_NAME\"",
      "rm -rf \".runtime/$AZEDARACH_ISSUE_ID\""
    ],
    "reconcileCommand": "just issue-resource-reconcile"
  }
}
```

`prepareCommands` run after the issue worktree is prepared and before the
tmux/agent launch. If a prepare command fails, session startup fails with the
command output in the diagnostic message. If resource prep or a later startup
step fails, `failedStartCleanupCommands` run best-effort and their failures are
reported.

`cleanupCommands` run only on explicit Azedarach lifecycle cleanup, such as
session stop or issue close cleanup paths. Azedarach does not infer destructive
cleanup when the section is absent or empty.

`reconcileCommand` is an optional idempotent command for richer resource
managers such as per-issue compose projects, cloud resources, env files, and
runtime directories. Azedarach runs it with
`AZEDARACH_RESOURCE_DESIRED_STATE=present` during session start and runtime
reconcile for non-closed issues with active runtime attachments. It runs with
`AZEDARACH_RESOURCE_DESIRED_STATE=absent` during issue close/delete cleanup,
before worktree removal, so project files are still available. A non-zero exit
status fails the lifecycle operation and the command output is surfaced as
evidence.

Resource commands and launched sessions receive these stable variables:

- `AZEDARACH_PROJECT_ID`
- `AZEDARACH_ISSUE_ID`
- `AZEDARACH_SESSION_ID`
- `AZEDARACH_WORKTREE_PATH`
- `AZEDARACH_ROOT_PATH`
- `AZEDARACH_BRANCH`
- `AZEDARACH_RESOURCE_DESIRED_STATE` (`present` or `absent`, only for
  `reconcileCommand`)

Values in `issueResources.env` may reference these variables with shell-style
`$NAME` expansion before they are exported. `AZEDARACH_*` names are reserved for
Azedarach lifecycle context and cannot be overridden from project config. Keep
resource names issue-scoped and make cleanup commands target only those names.
If `reconcileCommand` and one-shot prepare/cleanup commands are both configured,
use `az issue doctor`/review output to verify the mixed ownership is intentional.
