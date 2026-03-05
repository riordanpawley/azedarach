# Install `az` (ts-opentui)

> Fast path for coworkers who want a global `az` command quickly.

## Recommended Fast Path (Single-File Executable)

From the repo root:

```bash
git clone git@github.com:steveyegge/azedarach.git
cd azedarach
bun install
just install-sfe-ts
az --help
```

What this does:
- builds a single-file executable at `ts-opentui/bin/az`
- links `az` into a writable bin directory (`$AZ_LINK_DIR`, existing `az` dir, Homebrew bin, or `~/.local/bin`)

## Manual Fallback (No `just`)

```bash
git clone git@github.com:steveyegge/azedarach.git
cd azedarach/ts-opentui
bun install
bun run build-sfe
mkdir -p "$HOME/.local/bin"
ln -sf "$(pwd)/bin/az" "$HOME/.local/bin/az"
az --help
```

If `az` is not found after install, ensure `~/.local/bin` is on your `PATH`.

## Runtime Prerequisites

`az` depends on these tools at runtime:
- `tmux`
- `gh` (authenticated)
- `linear-cli` (authenticated/configured)
- `claude` (authenticated)

Quick macOS bootstrap:

```bash
brew install bun just tmux gh
# install/configure linear-cli and claude separately
```

## Updating

From your local clone:

```bash
git pull --rebase
bun install
just install-sfe-ts
```

## Brew Tap Brainstorm: Could/Should We?

### Could we?

Yes. A Homebrew tap is feasible if we publish release artifacts and checksums.

Minimum pieces:
1. Build release binaries for each target (`darwin-arm64`, `darwin-amd64`, and optionally Linux).
2. Publish them to GitHub Releases.
3. Maintain a tap repo (for example `homebrew-azedarach`) with an `az` formula.
4. Update SHA256 values for each release (ideally via CI automation).

### Should we right now?

Probably not yet, unless we commit to a release pipeline.

Reasoning:
- `just install-sfe-ts` is already a low-friction internal fast path.
- Tap maintenance adds release overhead (multi-arch builds, checksums, formula updates, support).
- If the binary/install flow is still changing quickly, a tap creates churn.

Recommended trigger to start a tap:
- at least a few regular coworker users and
- a repeatable release process (tag + artifacts + checksum automation).

### Proposed phased approach

1. Keep `just install-sfe-ts` as the default internal path now.
2. Add automated tagged releases for compiled binaries.
3. Introduce a tap after release automation is stable.
