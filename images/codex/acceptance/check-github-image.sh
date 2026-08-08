#!/bin/sh
set -eu

if [ "$#" -ne 2 ] || { [ "$1" != direct ] && [ "$1" != staged ]; }; then
  echo "usage: check-github-image.sh direct|staged ROOT" >&2
  exit 64
fi

mode=$1
root=$2
case "$root" in
  /) prefix= ;;
  /*) prefix=${root%/} ;;
  *) echo "GitHub image check root must be absolute" >&2; exit 64 ;;
esac

: "${GITHUB_PERSONAL_ACCESS_TOKEN:?runtime-only fake marker is required}"
: "${EXPECTED_GITHUB_SHA256:?expected pinned executable SHA-256 is required}"

expected_runtime_marker=$GITHUB_PERSONAL_ACCESS_TOKEN
export expected_runtime_marker
/bin/sh -c 'test "$GITHUB_PERSONAL_ACCESS_TOKEN" = "$expected_runtime_marker"'

config="$prefix/workspace/.codex/config.toml"
grep -F '[mcp_servers."github"]' "$config" >/dev/null
grep -F 'args = ["stdio"]' "$config" >/dev/null
grep -F 'env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]' "$config" >/dev/null
github_command=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^command = /{line=$0; sub(/^command = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
github_cwd=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^cwd = /{line=$0; sub(/^cwd = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
test -x "$github_command"
test -d "$github_cwd"
test "$github_cwd" = "$(dirname "$github_command")"

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256=$(sha256sum "$github_command" | awk '{print $1}')
else
  actual_sha256=$(shasum -a 256 "$github_command" | awk '{print $1}')
fi
test "$actual_sha256" = "$EXPECTED_GITHUB_SHA256"

assert_marker_absent() {
  for path in "$@"; do
    if grep -R -F "$GITHUB_PERSONAL_ACCESS_TOKEN" "$path"; then
      echo "GitHub $mode image persisted the runtime-only fake marker" >&2
      exit 1
    fi
  done
}

case "$mode:$github_command" in
  direct:"$prefix"/home/hctl/.config/hctl/integrations/prepared/*/github-mcp-server)
    assert_marker_absent "$prefix/agent" "$prefix/workspace" "$prefix/home/hctl/.config/hctl/integrations"
    ;;
  staged:"$prefix"/opt/hctl/integrations/github-mcp-server/*/linux-amd64/github-mcp-server)
    test ! -e "$prefix/home/hctl/.config/hctl/integrations"
    assert_marker_absent "$prefix/opt" "$prefix/workspace"
    ;;
  *)
    echo "GitHub $mode image uses an unexpected executable path" >&2
    exit 1
    ;;
esac
