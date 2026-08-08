#!/bin/sh
set -eu

module_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
version=
revision=
target=
output=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version=$2; shift 2 ;;
    --revision) revision=$2; shift 2 ;;
    --target) target=$2; shift 2 ;;
    --output) output=$2; shift 2 ;;
    *) echo "usage: build-package.sh --version VERSION --revision REVISION --target OS-ARCH --output DIR" >&2; exit 64 ;;
  esac
done

[ -n "$version" ] && [ -n "$revision" ] && [ -n "$target" ] && [ -n "$output" ] || {
  echo "usage: build-package.sh --version VERSION --revision REVISION --target OS-ARCH --output DIR" >&2
  exit 64
}
[ ! -e "$output" ] || { echo "package output already exists" >&2; exit 1; }

case "$target" in
  darwin-amd64) target_os=darwin; target_arch=amd64 ;;
  darwin-arm64) target_os=darwin; target_arch=arm64 ;;
  linux-amd64) target_os=linux; target_arch=amd64 ;;
  linux-arm64) target_os=linux; target_arch=arm64 ;;
  *) echo "unsupported Discord adapter target: $target" >&2; exit 1 ;;
esac

stage=$(mktemp -d "${TMPDIR:-/tmp}/hctl-discord-package.XXXXXX")
cleanup() { rm -rf -- "$stage"; }
trap cleanup EXIT HUP INT TERM
mkdir -p "$stage/payload" "$stage/licenses"
cp "$module_root/THIRD_PARTY_LICENSES.md" "$stage/licenses/THIRD_PARTY_LICENSES.md"
(
  cd "$module_root"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -buildvcs=false -ldflags=-buildid= -o "$stage/payload/hctl-discord" ./cmd/hctl-discord
)
size=$(wc -c < "$stage/payload/hctl-discord" | tr -d ' ')
sha256=$(shasum -a 256 "$stage/payload/hctl-discord" | awk '{print $1}')

sed \
  -e "s/@VERSION@/$version/g" \
  -e "s/@REVISION@/$revision/g" \
  -e "s/@TARGET@/$target/g" \
  -e "s/@OS@/$target_os/g" \
  -e "s/@ARCH@/$target_arch/g" \
  -e "s/@SIZE@/$size/g" \
  -e "s/@SHA256@/$sha256/g" \
  "$module_root/integration.template.json" > "$stage/integration.json"
chmod 0755 "$stage/payload/hctl-discord"
mkdir -p "$(dirname -- "$output")"
mv "$stage" "$output"
trap - EXIT HUP INT TERM
