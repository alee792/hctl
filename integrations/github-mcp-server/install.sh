#!/bin/sh
set -eu

package_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo_root=$(CDPATH= cd -- "$package_root/../.." && pwd -P)

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) platform=darwin-arm64 ;;
  Linux-x86_64) platform=linux-amd64 ;;
  *) echo "the curated GitHub MCP package does not support $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

hctl_executable=${HCTL_EXECUTABLE:-hctl}
command -v "$hctl_executable" >/dev/null 2>&1 || {
  echo "hctl is required; set HCTL_EXECUTABLE to its exact path" >&2
  exit 1
}

work=$(mktemp -d "${TMPDIR:-/tmp}/hctl-github-mcp-server.XXXXXX")
cleanup() {
  rm -rf -- "$work"
}
trap cleanup EXIT HUP INT TERM

"$repo_root/scripts/materialize-integration-package.sh" \
  --package "$package_root" --platform "$platform" --output "$work/package"
"$hctl_executable" integration install "$work/package" --trust operator
