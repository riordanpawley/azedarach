---
name: go-testing
description: Design, write, review, debug, and organize robust Go tests and test suites. Use for Go unit, component, integration, subprocess, concurrent, race, fuzz, property, benchmark, coverage, table-driven, golden, or Bubble Tea tests; for choosing what to test; for safe t.Parallel adoption; for eliminating flaky timing-based tests; or for structuring Go validation profiles and CI commands.
---

# Go Testing

Treat tests as executable specifications of observable behavior and durable invariants. Match the test layer to the production boundary, and prefer deterministic evidence over scheduler or wall-clock luck.

## Start with the repository contract

1. Read the applicable `AGENTS.md` and project test documentation.
2. Inspect analogous tests, helpers, fixtures, and validation commands before adding a new pattern.
3. Identify the behavior, invariant, regression, or risk the test must protect.
4. Select the narrowest layer that can prove it without bypassing the active production path.
5. Follow repository commands for broad validation; do not substitute a raw or focused command for a stronger named profile.

Repository instructions override generic examples in this skill.

## Choose what to test

Prioritize:

- domain rules and state transitions;
- public and cross-package contracts;
- parsing, serialization, persistence, protocol, and subprocess boundaries;
- error, cancellation, rollback, cleanup, recovery, and restart behavior;
- concurrency ownership and ordering invariants;
- previously observed regressions;
- properties with large input spaces;
- performance only where a regression would matter operationally.

Usually avoid tests for trivial accessors, Go language behavior, third-party implementation details, or private call sequences with no observable contract.

Use coverage to locate untested risk, not as a quality target. A high percentage with weak assertions is not strong evidence.

## Select the test layer

- **Domain/unit:** Pure rules, transformations, validation, and state transitions. Prefer direct inputs and outputs.
- **Component/store:** Persistence, adapters, lifecycle boundaries, and small collaborations. Exercise real storage when storage semantics matter.
- **Black-box package:** Put tests in `package foo_test` when the exported API or architectural boundary is the contract.
- **Integration/active path:** Exercise real wiring, processes, filesystems, protocols, or commands when isolated tests cannot prove production behavior.
- **Race:** Run shared-state and lifecycle-heavy paths with `-race`; remember it only detects races on executed paths.
- **Fuzz/property:** Use for parsers, codecs, normalization, import/export, graph transforms, and other broad input spaces.
- **Benchmark:** Measure important operations separately from correctness. Compare repeated samples statistically in a controlled environment.

Do not mock away the behavior under test. Accept narrow interfaces at boundaries and prefer small handwritten fakes over expectation-heavy mocks.

## Structure tests around behavior

Name tests for the contract and condition, for example `TestStartSessionRejectsStoppedIssue`. Use table-driven subtests when cases share setup and assertion shape but represent meaningful behaviors.

```go
func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Status
	}{
		{name: "ready", in: "ready", want: StatusReady},
		{name: "trimmed", in: " ready ", want: StatusReady},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeStatus(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeStatus(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}
```

Keep failures diagnostic:

- report operation, inputs, `got`, and `want`;
- use `Fatal` only when continuing would be meaningless;
- mark reusable helpers with `t.Helper()`;
- keep fixtures minimal and model the intended production lifecycle state;
- put opaque reusable inputs beneath `testdata/`;
- use `t.Cleanup`, `t.TempDir`, `t.Setenv`, and `t.Context` for scoped resources;
- prefer structural assertions over snapshots, and use goldens for intentionally stable rendered or serialized output.

Write a regression test that fails for the demonstrated defect before fixing it when practical. Do not force test-first sequencing when discovery, refactoring, or existing behavior makes characterization tests more honest.

## Parallelize only isolated tests

Distinguish the two controls:

- `go test -p=N` controls concurrent package build/test processes.
- `go test -parallel=N` limits parallel tests inside each test binary.

Call `t.Parallel()` only after proving isolation from:

- package globals and singleton caches;
- environment variables and working directory;
- shared database files, fixtures, and migration authorities;
- fixed ports, sockets, process names, tmux sessions, and external services;
- shared mock state, log collectors, output buffers, and mutable test data.

Do not use `t.Setenv` or `t.Chdir` in a parallel test or below a parallel ancestor. Copy loop values and mutable inputs when required by the Go version and test shape. Group parallel subtests under a parent when teardown must wait for the entire group.

Use `-shuffle=on` diagnostically to expose order dependence and retain the reported seed for reproduction. Never add parallelism solely to shorten an already contention-heavy suite; measure the whole suite and preserve resource isolation first.

## Test concurrent code deterministically

Do not use sleeps, short deadlines, elapsed-time assertions, scheduler fairness, CPU throughput, or reduced `GOMAXPROCS` to prove correctness.

Prefer:

- explicit start, progress, and completion barriers;
- controlled channels and acknowledgements;
- fake clocks and injected timers;
- context cancellation with observable cleanup;
- hooks or probes at durable state transitions;
- `testing/synctest` for eligible goroutine, timer, and context behavior.

Use operational test-command timeouts only to contain hangs and obtain goroutine stacks. Do not treat the timeout duration as semantic evidence.

With current Go releases, `testing/synctest.Test` creates an isolated bubble with virtual time, and `synctest.Wait` waits for goroutines in that bubble to block durably. Prefer it when all relevant concurrency stays inside the bubble; use explicit barriers for subprocesses, syscalls, or external runtimes it cannot control.

Always verify relevant concurrent paths with the race detector. A green race run is evidence only for executed paths, not proof of race freedom.

## Use fuzzing and properties deliberately

Define a durable property rather than merely asserting “does not panic.” Examples:

- decode(encode(x)) preserves x;
- normalization is idempotent;
- parse/render round trips preserve semantics;
- invalid input cannot escape validation or corrupt state;
- output ordering remains deterministic.

Seed empty, minimal, boundary, Unicode, malformed, and previously failing cases. Keep each fuzz invocation independent of persistent global state because fuzz workers execute concurrently and nondeterministically. Commit minimized failures as regression corpus entries when appropriate.

## Test Bubble Tea models

Exercise `Init`, `Update`, nested message routing, command emission, and `View` separately where that separation clarifies the contract.

- Assert state transitions after concrete messages.
- Execute returned commands only when command behavior is part of the test.
- Keep shared state explicit when testing nested models.
- Use structural assertions for model state and targeted assertions for view content.
- Use goldens for stable full rendering, including required default and constrained viewport cases.
- Avoid terminal-size globals and real input loops in model tests.

## Diagnose failures as a batch

For a focused failure, run the exact package or test uncached. For a hang, add a short command-level `-timeout` to obtain goroutine stacks and inspect the blocked call.

For a broad failure:

1. Preserve the complete machine-readable run, normally with `go test -json ... -count=1` or the repository wrapper.
2. Extract every failed package and test before editing.
3. Classify failures by shared root cause.
4. Fix coherent classes together, preferring a production-boundary or shared-fixture correction.
5. Rerun the complete relevant suite; focused green tests do not replace broad evidence.
6. Classify unrelated or flaky failures explicitly instead of silently rerunning until green.

## Choose commands honestly

Use repository-provided commands when available. Otherwise adapt these patterns:

```bash
go test -count=1 ./path/to/pkg -run '^TestName$/case$'
go test -json -count=1 ./... > /tmp/go-test-events.jsonl
go test -race -count=1 ./path/to/concurrent/packages/...
go test -shuffle=on -count=1 ./...
go test -fuzz '^FuzzName$' -run '^$' ./path/to/pkg
go test -bench '^BenchmarkName$' -run '^$' ./path/to/pkg
```

Treat focused, cached, race, fuzz, integration, benchmark, and full correctness profiles as different evidence. Never present one as another.

## Review checklist

- Does the test prove a requirement, invariant, regression, or meaningful risk?
- Is it at the model-correct layer and wired through the real boundary when required?
- Can it fail for the intended reason, with useful diagnostics?
- Are success, failure, cancellation, cleanup, and recovery cases covered where relevant?
- Is all mutable state isolated, especially under `t.Parallel` or fuzzing?
- Is concurrency controlled by state transitions instead of time?
- Are helpers smaller and clearer than the behavior they hide?
- Did the test fail before the fix when practical and pass afterward?
- Were targeted checks followed by the complete relevant repository profile?
- Were race, fuzz, integration, migration, golden, or benchmark profiles run when their risk applies?

## Authoritative references

Consult current official documentation when Go-version behavior matters:

- `https://pkg.go.dev/testing`
- `https://pkg.go.dev/testing/synctest`
- `https://pkg.go.dev/cmd/go`
- `https://go.dev/doc/articles/race_detector`
- `https://go.dev/doc/security/fuzz/`
- `https://go.dev/doc/build-cover`
