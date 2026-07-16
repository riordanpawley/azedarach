# Bounded worktree-aware Go cache protocol

## Ownership layout

The repository family owns one external cache root at
`<main>/.azedarach/go`. `GOPATH=<root>/path` remains shared so module downloads
are reused. Compiled objects live at
`<root>/caches/v1/<kind>/<owner>`:

- kind is `normal`, `race`, or `coverage`;
- owner is `main` for the root integration checkout or `issue-<id>` for a
  linked issue worktree;
- `.envrc` derives the owner from Git worktree identity, preferring the durable
  ticket environment/branch ID, and exports `-trimpath` through `GOFLAGS`.

The layout is central and lifecycle-scoped. Removing a checkout-local directory
cannot orphan ownership metadata, and race or coverage instrumentation cannot
contaminate ordinary development objects.

`AZEDARACH_GO_CACHE_ROOT` and `AZEDARACH_GOCACHE` cannot redirect a shell
outside this layout; when set, they must exactly match the derived project root
and namespace. This keeps shell use, validation telemetry, and daemon lifecycle
cleanup on the same owned bytes.

## Validation and limits

The outer `scripts/with-machine-validation-lease` wrapper is a thin client of
the daemon-owned repository-family validation queue. Aggregate work is
exclusive; shared focused validators may run concurrently until an aggregate
waiter reaches the head of the fair durable queue. When shared work is already
active, one later shared request may bypass the oldest aggregate; durable
request history then closes the shared lane until that aggregate runs, even
across daemon replacement. Explicitly safe work may run
beside an aggregate owner. The wrapper acquires before `go run` compilation,
heartbeats the durable request, and nested managed commands join its request.
The safe lane is bounded to daemon-recognized non-compiling command/profile
pairs; callers cannot relabel focused or aggregate Go work as safe.

Admission class is independent from attribution and evidence authority.
Repository-scoped push gates carry no ticket identity and can never authorize
ticket review or integration. Ticket-scoped development runs require an
existing ticket but remain development evidence. Only explicit
`review_evidence` purpose can enter review-readiness queries, and acquisition
binds it to the current ticket review epoch, reviewer lease, and source
revision. Nested recipes inherit class, scope, purpose, and revision from the
outer capability and cannot upgrade them. Rows predating this contract retain
`legacy` purpose and remain diagnostic history only.

The cache lock has a narrower role. Validators hold it shared while cache
maintenance holds it exclusively, so
`go clean -cache` never runs concurrently with managed validation. Reports use schema
`azedarach.test_timing_report.v3` and distinguish test-result cache policy from
the retained build cache. The `build_cache` object includes namespace, path,
policy, bytes/files before and after, deltas, total family bytes, configured
limits, and the resulting decision.

The daemon's operations database durably retains owners, waiters, request
identity, class/profile/command, source revision, heartbeat expiry, outcome,
and machine-load evidence. `just validation-status` and
`just validation-watch` expose this projection without compiling Go code.
Project orchestration snapshots also include the same validation-capacity
projection so active owners and waiters are visible beside worker capacity.
Expired heartbeats terminalize stale owners and transactionally wake the next
eligible waiter after process death or daemon restart.

The protocol-49 rollout is a one-generation bootstrap: the integration owner
must keep the pre-existing isolated canonical gate, validate the candidate
without invoking the new outer wrapper, integrate and install that managed
generation, then use the daemon queue for every later gate. Internal
`*_unleased` recipes are only nested implementation details after rollout and
must never be started as independent validation commands.

Defaults are a 10 GiB soft warning and a 28 GiB hard refusal. Configure exact
byte values with `AZEDARACH_GO_CACHE_SOFT_LIMIT_BYTES` and
`AZEDARACH_GO_CACHE_HARD_LIMIT_BYTES`. A command above the hard limit refuses
before validation. `AZEDARACH_GO_CACHE_AUTO_MAINTAIN=1` permits supported
cleanup of the selected namespace while holding the lock; a run that crosses
the hard limit still fails after writing its report.

`just test-race` selects the race namespace. `just test-coverage` selects the
coverage namespace. Other canonical profiles use normal build objects.

## Lifecycle and explicit maintenance

After daemon-authoritative worktree deletion succeeds, Azedarach finalizes the
lock and durable worktree projection, then cleans that inactive issue owner's
normal, race, and coverage namespaces. The standalone owner cleanup command
checks live Git worktrees and tmux sessions and refuses a live owner.

Maintenance opens the root, layout, kind, and owner component-by-component
without following symlinks. It passes the already-open namespace descriptor to
`go clean -cache`, accounts through descriptors, and removes entries with
descriptor-relative operations. Ancestor symlinks and namespace swaps are
therefore rejected without redirecting deletion outside the owned root.

Use:

```bash
just go-cache-inventory
just go-cache-maintain
just go-cache-clean-owner dhc
just go-cache-clean-legacy --confirm
```

Inventory is JSON and includes the managed layout plus historical
`build-cache`, `.gocache`, and `.gopath` locations. Legacy cleanup is always
explicit. It uses `go clean -cache` for old build caches. The optional
`--include-gopath-modcache` runs `go clean -modcache` for legacy module
downloads, but intentionally preserves GOPATH binaries and unknown user files.

If lifecycle namespaces plus measured limits eventually fail the reuse or disk
targets, the next phase is a bounded Go 1.25 `GOCACHEPROG`; it is not needed to
enforce this protocol.
