#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
  echo "usage: ./scripts/prepare-codex-image-context.sh --hctl FILE --version VERSION --output DIR" >&2
  exit 64
}

if [ "$#" -ne 6 ] || [ "$1" != "--hctl" ] || [ -z "$2" ] || [ "$3" != "--version" ] || [ -z "$4" ] || [ "$5" != "--output" ] || [ -z "$6" ]; then
  usage
fi
hctl_executable=$2
version=$4
requested_output=$6

hctl_parent=$(CDPATH= cd -- "$(dirname -- "$hctl_executable")" && pwd -P) || {
  echo "hctl image input parent must exist" >&2
  exit 1
}
hctl_executable="$hctl_parent/$(basename -- "$hctl_executable")"

[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] || {
  echo "Codex image context preparation requires linux/amd64" >&2
  exit 1
}
[ -x "$hctl_executable" ] || {
  echo "hctl image input must be executable" >&2
  exit 1
}
[ "$("$hctl_executable" --version)" = "hctl $version" ] || {
  echo "hctl image input version does not match --version" >&2
  exit 1
}
if [ -e "$requested_output" ]; then
  echo "image context output already exists: $requested_output" >&2
  exit 1
fi
output_parent=$(CDPATH= cd -- "$(dirname -- "$requested_output")" && pwd -P)
output="$output_parent/$(basename -- "$requested_output")"
stage=$(mktemp -d "$output_parent/.hctl-codex-context.XXXXXX")
downloads=$(mktemp -d "${TMPDIR:-/tmp}/hctl-codex-downloads.XXXXXX")
cleanup() {
  rm -rf -- "$stage" "$downloads"
}
trap cleanup EXIT HUP INT TERM

manifest="$repo_root/images/inputs.json"
(cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs check "$manifest")
for component in codex deno go python uv; do
  (cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs fetch "$manifest" "$component" "$downloads/$component")
done

[ "$(tar -tzf "$downloads/codex")" = "codex-x86_64-unknown-linux-musl" ] || {
  echo "Codex archive layout changed" >&2
  exit 1
}
[ "$(unzip -Z1 "$downloads/deno")" = "deno" ] || {
  echo "Deno archive layout changed" >&2
  exit 1
}
[ "$(tar -tzf "$downloads/uv")" = "uv-x86_64-unknown-linux-gnu/
uv-x86_64-unknown-linux-gnu/uvx
uv-x86_64-unknown-linux-gnu/uv" ] || {
  echo "uv archive layout changed" >&2
  exit 1
}
for archive in go python; do
  tar -tzf "$downloads/$archive" | awk -v prefix="$archive/" 'index($0, prefix) != 1 || index($0, "../") != 0 { exit 1 }'
done

rootfs="$stage/rootfs"
mkdir -p \
  "$rootfs/etc/ssl/certs" \
  "$rootfs/home/hctl" \
  "$rootfs/opt/hctl/bin" \
  "$rootfs/opt/hctl/harness/bin" \
  "$rootfs/opt/hctl/runtimes/deno/bin" \
  "$rootfs/opt/hctl/runtimes/uv/bin" \
  "$rootfs/opt/hctl/runtimes"

cp "$hctl_executable" "$rootfs/opt/hctl/bin/hctl"
tar -xOzf "$downloads/codex" codex-x86_64-unknown-linux-musl >"$rootfs/opt/hctl/harness/bin/codex"
unzip -p "$downloads/deno" deno >"$rootfs/opt/hctl/runtimes/deno/bin/deno"
tar -xOzf "$downloads/uv" uv-x86_64-unknown-linux-gnu/uv >"$rootfs/opt/hctl/runtimes/uv/bin/uv"
tar -xzf "$downloads/go" -C "$rootfs/opt/hctl/runtimes" go
tar -xzf "$downloads/python" -C "$rootfs/opt/hctl/runtimes" python
cp "$rootfs/opt/hctl/runtimes/python/lib/python3.13/site-packages/pip/_vendor/certifi/cacert.pem" "$rootfs/etc/ssl/certs/ca-certificates.crt"
cp "$manifest" "$rootfs/opt/hctl/image-inputs.json"

"$repo_root/scripts/materialize-integration-package.sh" \
  --package "$repo_root/integrations/github-mcp-server" \
  --platform linux-amd64 \
  --output "$downloads/github-mcp-server-package"
HOME="$rootfs/home/hctl" XDG_CONFIG_HOME="$rootfs/home/hctl/.config" \
  "$hctl_executable" integration install "$downloads/github-mcp-server-package" --trust operator >/dev/null
chmod 755 \
  "$rootfs/opt/hctl/bin/hctl" \
  "$rootfs/opt/hctl/harness/bin/codex" \
  "$rootfs/opt/hctl/runtimes/deno/bin/deno" \
  "$rootfs/opt/hctl/runtimes/uv/bin/uv"
find "$stage" -exec touch -h -d @0 {} +

mv "$stage" "$output"
printf '%s\n' "prepared $output"
