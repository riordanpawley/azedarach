# Scheduled Project Scripts

Scheduled project scripts are daemon-owned project maintenance commands. They
are for recurring cleanup or reconciliation that should not depend on a human
remembering to run a command after issue work is closed.

Example Chefy prune configuration:

```json
{
  "scheduledScripts": {
    "scripts": [
      {
        "name": "closed-issue-prune",
        "command": "pnpm local:prune --execute --closed-issues",
        "enabled": true,
        "interval": "1h",
        "timeoutMs": 300000
      }
    ]
  }
}
```

Scripts are disabled unless `enabled` is true. `interval` accepts Go duration
strings such as `15m`, `1h`, or `24h`. `schedule` also accepts `@every
<duration>` as an alias for interval-style scheduling. Cron expressions are not
implemented yet.

By default, Azedarach prevents overlapping runs. If a script is still running
when its next interval is due, the daemon records a skipped run and schedules
the next interval. Set `allowOverlap: true` only for commands that are known to
be safe concurrently.

## Execution Context

The daemon runs each command through the project session shell with `-lc`.
`cwd` defaults to the project root. Relative `cwd` values resolve from the
project root, while absolute values are used as provided.

Configured environment values are available through:

- `scheduledScripts.env` for every script.
- `scheduledScripts.scripts[].env` for one script.

The daemon also exports:

- `AZEDARACH_PROJECT_ID`
- `AZEDARACH_ROOT_PATH`
- `AZEDARACH_SCHEDULED_SCRIPT_NAME`
- `AZEDARACH_SCRIPT_NAME`

Project-configured environment variables may reference those context variables
with shell-style `$NAME` expansion. `AZEDARACH_*` names are reserved and cannot
be overridden by project config.

## Status And Logs

Inspect status with:

```bash
az project scripts status
az project scripts status --json
az project scripts status closed-issue-prune
```

The status includes enabled/running state, next run time, last exit code, last
error, run/skip counts, and the last log path. Run logs are written under:

```text
.azedarach/scheduled-scripts/<project-id>/<script-name>.log
```

Scheduled scripts are a periodic backstop. They do not replace immediate
issue-resource cleanup on close/delete paths.
