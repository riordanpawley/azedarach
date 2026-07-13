# Test Wait Audit

This audit covers Go tests under `internal/` and `cmd/` as of 2026-07-13. A
wait is considered long when it can intentionally hold a passing test for more
than 500 ms. Synthetic timestamps such as `now.Add(5 * time.Minute)` are test
data, not waits, and are excluded.

## Removed elapsed waits

| Contract | Before | After | Replacement |
| --- | ---: | ---: | --- |
| Three issue-store SQLite busy-retry tests plus production-default assertions | 18.593 s | 3.131 s | Client-owned busy timeout/backoff plus a retry barrier; production remains 5 s/100 ms. The after run also opens two real stores to assert their SQLite pragmas. |
| IPC context deadline tests | 6.294 s | 0.505 s | Direct deadline inspection plus one 5 ms Unix-socket deadline smoke test; production remains 30 s. |
| Session monitor package | 4.421 s | 0.236 s | Client-owned poll interval and a manual ticker; production remains 500 ms. |
| CLI package | 25.202 s | 16.461 s | Single-attempt injected reconnect policies, configurable autostart backoffs, and correct typed read-timeout classification; production budgets/backoffs are unchanged. |

The timings are uncached `go test -json ... -count=1` package elapsed values on
the same worktree. Package totals include compilation and unrelated tests, so
the focused behavioral assertions are also run repeatedly and under `-race`.

## Remaining waits over 500 ms

The source inventory is:

- `time.Sleep` with a duration over 500 ms: **none**.
- `time.NewTimer` or `time.NewTicker` with a duration over 500 ms: **none**.
- `time.After` with seconds/minutes in tests: **88**, all select/watchdog failure
  ceilings. They return immediately when the expected event occurs and fail the
  test if the ceiling fires; they are not elapsed-time assertions.
- `context.WithTimeout` with seconds/minutes in tests: **27**, all bounded
  cancellation or real integration safety budgets. Passing fast-path tests do
  not wait for those budgets. Tests of timeout selection inspect the propagated
  deadline rather than waiting for it.

Re-run the inventory with:

```bash
rg -n 'time\.Sleep\([^\n]*(Second|Minute)|time\.Sleep\((?:[6-9][0-9]{2}|[1-9][0-9]{3,})\s*\*\s*time\.Millisecond' --pcre2 --glob '*_test.go' internal cmd
rg -n 'time\.After\([^\n]*(Second|Minute)' --glob '*_test.go' internal cmd
rg -n 'context\.WithTimeout\([^\n]*(Second|Minute)' --glob '*_test.go' internal cmd
```

## Required real-time smoke coverage

Two small OS contracts remain real-time:

1. `TestCommandTimesOutWhenContextDeadlineIsShorterThanClientTimeout` uses a
   5 ms context deadline against a Unix socket. A direct unit test proves which
   deadline is selected; this smoke test proves the operating system surfaces
   that deadline as `net.Error`.
2. The SQLite busy-retry tests use a 1 ms SQLite busy timeout to obtain a real
   `SQLITE_BUSY` result. Their injected retry barrier holds the retry until the
   test commits the lock, so they do not depend on a wall-clock sleep.

Removing either smoke layer would leave only fake-connection or synthetic-error
coverage for behavior implemented by the OS/SQLite driver.
