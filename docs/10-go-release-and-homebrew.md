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
   - The two version strings must match. After `direnv reload` in an Azedarach
     repository or linked worktree, `command -v az` and `command -v azd` must
     resolve inside the same active immutable `.azedarach-generations/generation.*`
     directory rather than the repository's preserved `bin/`, a scratch pair,
     or an older generation. Environment activation identifies the installer-owned
     `.azedarach-current` control links and strips other `az`/`azd` directories
     from that repository shell's `PATH`.
     Global daemon pairing trusts only a client whose resolved executable is
     inside `.azedarach-generations/generation.*`; primary-repo `bin/` binaries
     remain development artifacts.
   - Successful immutable generations are retained across later installs so
     long-lived clients keep access to their paired daemon. Do not manually
     remove generation directories while clients from them may still run.
3. If an older worktree-targeting symlink exists, migrate it to the stable,
   paired generation layout with the local helper:
   - `just build-install-run --no-run`
   - If the helper publishes the pair but reports that the caller remains
     shadowed, reload direnv, verify both `command -v` results, and rerun it. The
     non-success result is intentional: daemon replacement is withheld until
     the invoking shell resolves the newly published coherent generation.
   - A caller already running from a retained managed generation is safe but
     may become older than the newly published generation. The helper reports
     that distinction and asks for a reload without misclassifying it as an
     unmanaged shadowing failure.

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
