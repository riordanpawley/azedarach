#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Generate a Homebrew formula for Azedarach from release checksums.

Usage:
  generate-homebrew-formula.sh <tag> --output <path> [--repo <owner/repo>] [--sha256sums <path>]
EOF
}

if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  usage
  exit 0
fi

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
    -h|--help)
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
DARWIN_ARM64_D_SHA="$(extract_sha "azd-darwin-arm64")"
DARWIN_X64_D_SHA="$(extract_sha "azd-darwin-x64")"
LINUX_X64_D_SHA="$(extract_sha "azd-linux-x64")"

VERSION="${TAG#v}"

mkdir -p "$(dirname "$OUTPUT_PATH")"

cat >"$OUTPUT_PATH" <<EOF
class Azedarach < Formula
  desc "TUI Kanban board for orchestrating parallel AI sessions"
  homepage "https://github.com/$REPO"
  version "$VERSION"
  license "MIT"

  conflicts_with "azure-cli", because: "both install an executable named az"

  on_macos do
    on_arm do
      url "https://github.com/$REPO/releases/download/$TAG/az-darwin-arm64"
      sha256 "$DARWIN_ARM64_SHA"
      resource "azd-bin" do
        url "https://github.com/$REPO/releases/download/$TAG/azd-darwin-arm64"
        sha256 "$DARWIN_ARM64_D_SHA"
      end
    end

    on_intel do
      url "https://github.com/$REPO/releases/download/$TAG/az-darwin-x64"
      sha256 "$DARWIN_X64_SHA"
      resource "azd-bin" do
        url "https://github.com/$REPO/releases/download/$TAG/azd-darwin-x64"
        sha256 "$DARWIN_X64_D_SHA"
      end
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/$REPO/releases/download/$TAG/az-linux-x64"
      sha256 "$LINUX_X64_SHA"
      resource "azd-bin" do
        url "https://github.com/$REPO/releases/download/$TAG/azd-linux-x64"
        sha256 "$LINUX_X64_D_SHA"
      end
    end
  end

  def install
    az_binary = Dir["az-*"].first
    raise "Unable to find downloaded az binary" if az_binary.nil?
    bin.install az_binary => "az"
    resource("azd-bin").stage do
      azd_binary = Dir["azd-*"].first
      raise "Unable to find downloaded azd binary" if azd_binary.nil?
      bin.install azd_binary => "azd"
    end
  end

  test do
    assert_match "Usage", shell_output("#{bin}/az --help")
    assert_match "Usage", shell_output("#{bin}/azd --help")
  end
end
EOF

echo "Wrote formula to $OUTPUT_PATH"
