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

`AZEDARACH_GOCACHE` cannot redirect a shell outside this layout; when set, it
must exactly match the derived namespace. This keeps shell use, validation
telemetry, and daemon lifecycle cleanup on the same owned bytes.

## Validation and limits

`cmd/test-timing` and `cmd/go-cache run` take the repository-family exclusive
cache lock for the full managed validation. Maintenance takes the same lock, so
`go clean -cache` never runs concurrently with managed validation. Reports use schema
`azedarach.test_timing_report.v2` and distinguish test-result cache policy from
the retained build cache. The `build_cache` object includes namespace, path,
policy, bytes/files before and after, deltas, total family bytes, configured
limits, and the resulting decision.

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
