#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if [ "$#" -ne 2 ] || [ "$1" != "--output" ] || [ -z "$2" ]; then
  echo "usage: ./scripts/build-release-artifacts.sh --output DIR" >&2
  exit 64
fi
requested_output=$2

[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] || {
  echo "release artifact construction requires linux/amd64" >&2
  exit 1
}
command -v docker >/dev/null 2>&1 || {
  echo "Docker is required for release artifact construction" >&2
  exit 1
}
if [ -e "$requested_output" ]; then
  echo "release artifact output already exists: $requested_output" >&2
  exit 1
fi
output_parent=$(CDPATH= cd -- "$(dirname -- "$requested_output")" && pwd -P)
output="$output_parent/$(basename -- "$requested_output")"
stage=$(mktemp -d "$output_parent/.hctl-release-artifacts.XXXXXX")
build=$(mktemp -d "${TMPDIR:-/tmp}/hctl-release-build.XXXXXX")
cleanup() {
  rm -rf -- "$stage" "$build"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$stage/release" "$stage/transport"
"$repo_root/scripts/build-release-archive.sh" --output "$stage/release"
tag=$(git -C "$repo_root" tag --points-at HEAD --list 'v[0-9]*')
version=${tag#v}
archive="$stage/release/hctl_${version}_darwin_arm64.tar.gz"
mkdir "$build/archive"
tar -xzf "$archive" -C "$build/archive"
"$repo_root/scripts/build-hctl-binary.sh" \
  --target darwin-arm64 --version "$version" --output "$build/hctl_darwin_arm64_repeat"
cmp "$build/archive/hctl" "$build/hctl_darwin_arm64_repeat"

"$repo_root/scripts/build-hctl-binary.sh" \
  --target linux-amd64 --version "$version" --output "$build/hctl_linux_amd64"
"$repo_root/scripts/build-hctl-binary.sh" \
  --target linux-amd64 --version "$version" --output "$build/hctl_linux_amd64_repeat"
cmp "$build/hctl_linux_amd64" "$build/hctl_linux_amd64_repeat"
[ "$("$build/hctl_linux_amd64" --version)" = "hctl $version" ]
"$repo_root/scripts/check-codex-image.sh" \
  --hctl "$build/hctl_linux_amd64" --version "$version"

transport="hctl_${version}_codex_linux_amd64.docker.tar.gz"
docker image save --output "$build/codex-image.tar" hctl-codex:acceptance
gzip -n "$build/codex-image.tar"
mv "$build/codex-image.tar.gz" "$stage/transport/$transport"
gzip -t "$stage/transport/$transport"
(
  cd "$stage/transport"
  sha256sum "$transport" >"$transport.sha256"
)

mv "$stage" "$output"
printf '%s\n' "built $output/release"
printf '%s\n' "built $output/transport/$transport"
