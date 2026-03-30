# go-bubbletea

Go/Bubbletea implementation track for Azedarach.

## Homebrew Multi-Executable Packaging Plan

When the daemon is a separate executable, Homebrew packaging should keep
`az` and the daemon as separate binaries.

Recommended formula shape:

- Install user-facing CLI to `bin` as `az`.
- Install daemon binary to `libexec` (private runtime dependency).
- Launch daemon from CLI using a fixed `opt_libexec` path, not via `PATH`.
- Add a `service do` block only if we want persistent background operation
  through `brew services`.

Rule of thumb:

- Single formula when daemon is implementation-internal for `az`.
- Separate formula only if daemon is independently useful/versioned.
