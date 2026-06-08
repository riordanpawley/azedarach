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
    ]
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

Resource commands and launched sessions receive these stable variables:

- `AZEDARACH_PROJECT_ID`
- `AZEDARACH_ISSUE_ID`
- `AZEDARACH_SESSION_ID`
- `AZEDARACH_WORKTREE_PATH`
- `AZEDARACH_ROOT_PATH`
- `AZEDARACH_BRANCH`

Values in `issueResources.env` may reference these variables with shell-style
`$NAME` expansion before they are exported. Keep resource names issue-scoped and
make cleanup commands target only those names.
