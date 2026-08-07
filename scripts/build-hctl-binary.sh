#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
  echo "usage: ./scripts/build-hctl-binary.sh --target <darwin-arm64|linux-amd64> --version VERSION --output FILE" >&2
  exit 64
}

if [ "$#" -ne 6 ] || [ "$1" != "--target" ] || [ -z "$2" ] || [ "$3" != "--version" ] || [ -z "$4" ] || [ "$5" != "--output" ] || [ -z "$6" ]; then
  usage
fi
target=$2
version=$4
requested_output=$6

if ! (cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs validate-version "$version"); then
  echo "binary version must be an exact semantic version without a v prefix: $version" >&2
  exit 64
fi

case "$target" in
  darwin-arm64)
    target_os=darwin
    target_arch=arm64
    ;;
  linux-amd64)
    target_os=linux
    target_arch=amd64
    ;;
  *)
    usage
    ;;
esac

if [ -e "$requested_output" ]; then
  echo "binary output already exists: $requested_output" >&2
  exit 1
fi
output_parent=$(CDPATH= cd -- "$(dirname -- "$requested_output")" && pwd -P)
output="$output_parent/$(basename -- "$requested_output")"
stage=$(mktemp -d "$output_parent/.hctl-binary.XXXXXX")
cleanup() {
  rm -rf -- "$stage"
}
trap cleanup EXIT HUP INT TERM

(
  cd "$repo_root"
  GOOS=$target_os GOARCH=$target_arch CGO_ENABLED=0 \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-buildid= -X hctl/internal/version.Value=$version" \
    -o "$stage/hctl" ./cmd/hctl
)
[ -x "$stage/hctl" ] || {
  echo "binary build did not produce an executable" >&2
  exit 1
}
build_info=$(go version -m "$stage/hctl")
printf '%s\n' "$build_info" | grep -F "GOOS=$target_os" >/dev/null || {
  echo "binary build reported the wrong operating system" >&2
  exit 1
}
printf '%s\n' "$build_info" | grep -F "GOARCH=$target_arch" >/dev/null || {
  echo "binary build reported the wrong architecture" >&2
  exit 1
}

mv "$stage/hctl" "$output"
printf '%s\n' "built $output"
