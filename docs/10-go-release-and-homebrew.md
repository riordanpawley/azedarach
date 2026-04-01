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
3. If an older manual symlink exists, refresh links with local helper:
   - `just build-link-run -- --no-run`

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
