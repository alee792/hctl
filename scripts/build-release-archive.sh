#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
  echo "usage: ./scripts/build-release-archive.sh --output DIR" >&2
  exit 64
}

if [ "$#" -ne 2 ] || [ "$1" != "--output" ] || [ -z "$2" ]; then
  usage
fi
output_dir=$2

if git -C "$repo_root" symbolic-ref -q HEAD >/dev/null; then
  echo "release archive requires HEAD to be detached at an exact vX.Y.Z tag" >&2
  exit 1
fi
if [ -n "$(git -C "$repo_root" status --porcelain=v1 --untracked-files=all)" ]; then
  echo "release archive requires a clean tagged source" >&2
  exit 1
fi
version_tags=$(git -C "$repo_root" tag --points-at HEAD --list 'v[0-9]*' | sed '/^$/d')
if [ "$(printf '%s\n' "$version_tags" | wc -l | tr -d ' ')" -ne 1 ] ||
  ! printf '%s\n' "$version_tags" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "release archive requires exactly one vX.Y.Z tag on HEAD" >&2
  exit 1
fi
tag=$version_tags
version=${tag#v}
archive="hctl_${version}_darwin_arm64.tar.gz"
checksums="hctl_${version}_SHA256SUMS"

mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd -P)
if [ -e "$output_dir/$archive" ] || [ -e "$output_dir/$checksums" ]; then
  echo "release archive output already exists in $output_dir" >&2
  exit 1
fi
stage=$(mktemp -d "${TMPDIR:-/tmp}/hctl-release.XXXXXX")
cleanup() {
  rm -rf -- "$stage"
}
trap cleanup EXIT HUP INT TERM

"$repo_root/scripts/build-hctl-binary.sh" --target darwin-arm64 --version "$version" --output "$stage/hctl"
[ -x "$stage/hctl" ] || {
  echo "release archive did not produce an executable" >&2
  exit 1
}

go run -mod=readonly "$repo_root/scripts/release-archive.go" --input "$stage/hctl" --output "$output_dir/$archive"
[ "$(tar -tzf "$output_dir/$archive")" = "hctl" ] || {
  echo "release archive must contain only root-level hctl" >&2
  exit 1
}
(
  cd "$output_dir"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$archive" >"$checksums"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$archive" >"$checksums"
  else
    echo "a SHA-256 utility (shasum or sha256sum) is required" >&2
    exit 1
  fi
)

printf '%s\n' "built $output_dir/$archive"
printf '%s\n' "built $output_dir/$checksums"
