# Go Release and Homebrew

This repository now ships the Go implementation as the canonical `az` CLI.

## Formula Naming and UX

- Homebrew formula name: `azedarach`
- Installed binaries:
  - `az`
  - `azd`
- Existing behavior remains:
  - `az` is the primary CLI entrypoint.
  - `azd` is daemon runtime binary.

## Upgrade Path

1. Upgrade/install formula:
   - `brew tap riordanpawley/azedarach`
   - `brew upgrade azedarach` (or `brew install azedarach`)
2. Verify installed binaries:
   - `az --version`
   - `azd --version`
   - The two version strings must match. Global PATH resolves the stable
     installer-owned `/opt/homebrew/bin/az` control link; `build-install-run`
     atomically switches that link. Direnv does not select, prepend, or watch
     Azedarach runtime generations. Each running `az` resolves its matching
     sibling `azd` internally; repository and scratch binaries are never part
     of the production control path.
   - Successful immutable generations are retained across later installs so
     long-lived clients keep access to their paired daemon. Do not manually
     remove generation directories while clients from them may still run.
3. If an older worktree-targeting symlink exists, migrate it to the stable,
   paired generation layout with the local helper:
   - `just build-install-run --no-run`
   - The helper publishes the pair and atomically switches the stable control
     link; no direnv reload is required.
   - A caller already running from a retained managed generation is safe but
     may become older than the newly published generation. The helper reports
     that distinction and asks for a reload without misclassifying it as an
     unmanaged shadowing failure.

## Validation Boundary

The installed production `az` and global daemon own the repository-family lease
queue, validation admission, and durable evidence. Exact-source candidate
executables are payloads only: they run inside a private socket/runtime/cache
with temporary databases or private online-backup clones. Candidate processes
must not open production databases, projections, sockets, locks, or the global
daemon, and must never act as the validation control plane.

The validation wrapper keeps two explicit, non-interchangeable channels. The
installed production client (`AZEDARACH_VALIDATION_CONTROL_AZ_BIN`, with the
legacy `AZEDARACH_VALIDATION_AZ_BIN` name accepted for control only) performs
lease acquire, heartbeat, nested authorization, and finish with candidate
daemon routing removed. An explicitly worktree-scoped run must separately pin
`AZEDARACH_VALIDATION_CLEANUP_AZ_BIN` to the candidate-compatible cleanup
client; that client receives scoped routing but no production lease token or
request authority. Candidate daemon stop and private runtime removal complete
before the production finish RPC. Any cleanup error is written into terminal
evidence and forces a failed outcome even when the payload already failed.

After that candidate is integrated into `main`, production deployment remains
an explicit operator action from the primary worktree:

1. Run `just build-install-run --no-run` from the primary worktree.
2. Verify the stable control link and matching `az`/`azd` sibling resolution.
3. Restart the global daemon only as an explicit production deployment action.

This isolated candidate execution is not a production install path and does not
make deployment implicit.

## Release Commands

- Dry-run release orchestration:
  - `just release-homebrew -- --major --tap-dir ~/prog/homebrew-azedarach --skip-tap-commit --skip-tap-push -- --dry-run --skip-pull`
- Real release orchestration:
  - `just release-homebrew -- --major --tap-dir ~/prog/homebrew-azedarach`
  - Supported bump flags: exactly one of `--patch`, `--minor`, `--major`

## Rollback Playbook

If a release is bad, use this order:

1. Stop rollout in tap:
   - Revert the formula commit in `homebrew-azedarach` and push.
2. Hide/replace bad GitHub release:
   - Delete or recreate release assets/tag as needed.
3. Cut fixed release:
   - Re-run release flow with next version.
4. Confirm consumers:
   - `brew update && brew upgrade azedarach`
   - `az --version` / `azd --version` match fixed tag.

## CI Safety Nets

- Release workflow builds and publishes `az` + `azd` assets for macOS arm64/x64 and Linux x64.
- Post-release smoke checks download released assets and run:
  - `az --version`, `az --help`
  - `azd --version`, `azd --help`

## Config Schema for Homebrew Users

- Project configs write `$schema` as:
  - `https://raw.githubusercontent.com/riordanpawley/azedarach/main/docs/config.schema.json`
- This keeps JSON schema validation/autocomplete working for users installed via Homebrew, since the schema is fetched from a public URL instead of local repo paths.
