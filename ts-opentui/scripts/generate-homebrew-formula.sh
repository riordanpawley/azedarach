#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat <<'EOF'
Generate a Homebrew formula for Azedarach from release checksums.

Usage:
  generate-homebrew-formula.sh <tag> --output <path> [--repo <owner/repo>] [--sha256sums <path>]

Examples:
  ./ts-opentui/scripts/generate-homebrew-formula.sh v0.3.0 \
    --repo riordanpawley/azedarach \
    --output /tmp/homebrew-azedarach/Formula/azedarach.rb

  ./ts-opentui/scripts/generate-homebrew-formula.sh v0.3.0 \
    --output /tmp/azedarach.rb \
    --sha256sums /tmp/SHA256SUMS.txt
EOF
}

if [[ $# -lt 1 ]]; then
	usage
	exit 1
fi

TAG="$1"
shift

REPO="riordanpawley/azedarach"
OUTPUT_PATH=""
SHA256SUMS_PATH=""

while [[ $# -gt 0 ]]; do
	case "$1" in
	--repo)
		REPO="$2"
		shift 2
		;;
	--output)
		OUTPUT_PATH="$2"
		shift 2
		;;
	--sha256sums)
		SHA256SUMS_PATH="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "Unknown argument: $1" >&2
		usage
		exit 1
		;;
	esac
done

if [[ -z "$OUTPUT_PATH" ]]; then
	echo "--output is required" >&2
	usage
	exit 1
fi

TMP_DIR=""
cleanup() {
	if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
		rm -rf "$TMP_DIR"
	fi
}
trap cleanup EXIT

if [[ -z "$SHA256SUMS_PATH" ]]; then
	TMP_DIR="$(mktemp -d /tmp/az-homebrew-formula-XXXXXX)"
	gh release download "$TAG" --repo "$REPO" --pattern "SHA256SUMS.txt" --dir "$TMP_DIR" --clobber
	SHA256SUMS_PATH="$TMP_DIR/SHA256SUMS.txt"
fi

if [[ ! -f "$SHA256SUMS_PATH" ]]; then
	echo "SHA256SUMS file not found: $SHA256SUMS_PATH" >&2
	exit 1
fi

extract_sha() {
	local asset="$1"
	local sha
	sha="$(awk -v target="$asset" '$2==target { print $1; exit }' "$SHA256SUMS_PATH")"

	if [[ -z "$sha" ]]; then
		echo "Missing checksum for asset: $asset" >&2
		exit 1
	fi

	echo "$sha"
}

DARWIN_ARM64_SHA="$(extract_sha "az-darwin-arm64")"
DARWIN_X64_SHA="$(extract_sha "az-darwin-x64")"
LINUX_X64_SHA="$(extract_sha "az-linux-x64")"

VERSION="${TAG#v}"

mkdir -p "$(dirname "$OUTPUT_PATH")"

cat >"$OUTPUT_PATH" <<EOF
class Azedarach < Formula
  desc "TUI Kanban board for orchestrating parallel Claude Code sessions"
  homepage "https://github.com/$REPO"
  version "$VERSION"
  license "MIT"

  conflicts_with "azure-cli", because: "both install an executable named az"

  on_macos do
    on_arm do
      url "https://github.com/$REPO/releases/download/$TAG/az-darwin-arm64"
      sha256 "$DARWIN_ARM64_SHA"
    end

    on_intel do
      url "https://github.com/$REPO/releases/download/$TAG/az-darwin-x64"
      sha256 "$DARWIN_X64_SHA"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/$REPO/releases/download/$TAG/az-linux-x64"
      sha256 "$LINUX_X64_SHA"
    end
  end

  def install
    binary = Dir["az-*"].first
    raise "Unable to find downloaded az binary" if binary.nil?

    bin.install binary => "az"
  end

  test do
    assert_match "Usage", shell_output("#{bin}/az --help")
  end
end
EOF

echo "Wrote formula to $OUTPUT_PATH"
