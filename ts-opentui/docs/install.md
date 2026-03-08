# Install `az` (ts-opentui)

> Fast path for coworkers who want a global `az` command quickly.

## Fastest macOS Path (Homebrew Tap)

```bash
brew tap riordanpawley/azedarach
brew install azedarach
az --help
```

Note:
- this installs an `az` binary and conflicts with `azure-cli` (same executable name).

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
- `az` (authenticated/configured)
- `claude` (authenticated)

Quick macOS bootstrap:

```bash
brew install bun just tmux gh
# install/configure az and claude separately
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

## Automated Releases (Maintainers)

Releases are automated via GitHub Actions in `.github/workflows/release-az-binaries.yml`.

Trigger:
- run the release script (which bumps `package.json`, commits, tags, and pushes)

What it publishes:
- `az-darwin-arm64`
- `az-darwin-x64`
- `az-linux-x64`
- per-binary `.sha256` files
- `SHA256SUMS.txt`

Example release cut:

```bash
git checkout main
git pull --rebase
just release-ts-opentui patch
```

One-step release + Homebrew tap update (maintainers):

```bash
just release-ts-opentui-homebrew patch
```

By default this uses:
- `/Users/riordan/prog/homebrew-azedarach`

Override if needed:

```bash
AZ_HOMEBREW_TAP_DIR=/path/to/homebrew-azedarach \
  just release-ts-opentui-homebrew patch
```

Release script behavior:
- validates clean working tree on `main`
- runs `bun run type-check` in `ts-opentui`
- updates `ts-opentui/package.json` to the requested version
- commits `release: vX.Y.Z`
- creates annotated tag `vX.Y.Z`
- pushes both `main` and the tag

Accepted bump targets:
- `patch`
- `minor`
- `major`

## Homebrew Tap Setup (Maintainers)

Status:
- release artifacts are automated
- tap repository and formula publishing are manual

Tap repository:
- `https://github.com/riordanpawley/homebrew-azedarach`

Bootstrap steps:

1. Create and clone the tap repository:
   ```bash
   gh repo create <owner>/homebrew-azedarach --public
   git clone git@github.com:<owner>/homebrew-azedarach.git
   ```
2. Generate a formula from a release tag:
   ```bash
   ./ts-opentui/scripts/generate-homebrew-formula.sh v0.3.1 \
     --repo riordanpawley/azedarach \
     --output /path/to/homebrew-azedarach/Formula/azedarach.rb
   ```
3. Commit and push in the tap repo:
   ```bash
   cd /path/to/homebrew-azedarach
   git add Formula/azedarach.rb
   git commit -m "azedarach v0.3.1"
   git push
   ```

Shortcut:
- `just release-ts-opentui-homebrew patch` runs release + formula generation and commits/pushes the tap update (default tap path: `/Users/riordan/prog/homebrew-azedarach`).

Coworker install (after tap is published):

```bash
brew tap riordanpawley/azedarach
brew install azedarach
az --help
```

Note:
- this formula installs an `az` binary and declares `conflicts_with "azure-cli"`.
