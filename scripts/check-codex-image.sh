#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
manifest="$repo_root/images/inputs.json"

if { [ "$#" -ne 2 ] && [ "$#" -ne 4 ]; } || [ "$1" != "--hctl" ] || [ -z "$2" ] || { [ "$#" -eq 4 ] && { [ "$3" != "--version" ] || [ -z "$4" ]; }; }; then
  echo "usage: ./scripts/check-codex-image.sh --hctl FILE [--version VERSION]" >&2
  exit 64
fi
hctl_executable=$2
hctl_version_override=
if [ "$#" -eq 4 ]; then
  hctl_version_override=$4
fi
hctl_parent=$(CDPATH= cd -- "$(dirname -- "$hctl_executable")" && pwd -P) || {
  echo "hctl image input parent must exist" >&2
  exit 1
}
hctl_executable="$hctl_parent/$(basename -- "$hctl_executable")"

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required for Codex image acceptance" >&2
  exit 1
}
[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] || {
  echo "Codex image acceptance requires linux/amd64" >&2
  exit 1
}

(cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs check "$manifest")
work=$(mktemp -d "${TMPDIR:-/tmp}/hctl-codex-image.XXXXXX")
cleanup() {
  rm -rf -- "$work"
}
trap cleanup EXIT HUP INT TERM

(cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs metadata "$manifest") >"$work/metadata.tsv"
tab=$(printf '\t')
while IFS="$tab" read -r key value; do
  case "$key" in
    base_reference) base_reference=$value ;;
    base_digest) base_digest=$value ;;
    hctl_version) manifest_hctl_version=$value ;;
    shared_libraries) shared_libraries=$value ;;
    codex_version) codex_version=$value ;;
    codex_sha256) codex_sha256=$value ;;
    deno_version) deno_version=$value ;;
    deno_sha256) deno_sha256=$value ;;
    python_version) python_version=$value ;;
    python_sha256) python_sha256=$value ;;
    uv_version) uv_version=$value ;;
    uv_sha256) uv_sha256=$value ;;
    go_version) go_version=$value ;;
    go_sha256) go_sha256=$value ;;
    *) echo "unexpected image metadata field: $key" >&2; exit 1 ;;
  esac
done <"$work/metadata.tsv"
hctl_version=${hctl_version_override:-$manifest_hctl_version}
(cd "$repo_root" && go run -mod=readonly ./scripts/image-inputs validate-version "$hctl_version")
base_image="$base_reference@$base_digest"

"$repo_root/scripts/prepare-codex-image-context.sh" --hctl "$hctl_executable" --version "$hctl_version" --output "$work/context"

source_image="hctl-codex:acceptance"
direct_image="hctl-codex-direct:acceptance"
staged_image="hctl-codex-staged:acceptance"
revision=$(git -C "$repo_root" rev-parse HEAD)

docker build --platform linux/amd64 --pull \
  --build-arg "BASE_IMAGE=$base_image" \
  --build-arg "HCTL_VERSION=$hctl_version" \
  --build-arg "HCTL_SHA256=$(sha256sum "$hctl_executable" | awk '{print $1}')" \
  --build-arg "SOURCE_REVISION=$revision" \
  --build-arg "BASE_REFERENCE=$base_reference" \
  --build-arg "BASE_DIGEST=$base_digest" \
  --build-arg "INPUTS_SHA256=$(sha256sum "$manifest" | awk '{print $1}')" \
  --build-arg "CODEX_VERSION=$codex_version" \
  --build-arg "CODEX_SHA256=$codex_sha256" \
  --build-arg "DENO_VERSION=$deno_version" \
  --build-arg "DENO_SHA256=$deno_sha256" \
  --build-arg "PYTHON_VERSION=$python_version" \
  --build-arg "PYTHON_SHA256=$python_sha256" \
  --build-arg "UV_VERSION=$uv_version" \
  --build-arg "UV_SHA256=$uv_sha256" \
  --build-arg "GO_VERSION=$go_version" \
  --build-arg "GO_SHA256=$go_sha256" \
  --tag "$source_image" \
  --file "$repo_root/images/codex/Dockerfile" \
  "$work/context"

docker run --rm --network none --entrypoint /bin/sh --env "EXPECTED_SHARED_LIBRARIES=$shared_libraries" "$source_image" -c '
  set -eux
  test "$(id -u):$(id -g)" = "65532:65532"
  test "$(hctl --version)" = "hctl '"$hctl_version"'"
  test -d /home/hctl/.codex
  test -z "$(find /home/hctl/.codex -mindepth 1 -print -quit)"
  test -z "$(find /home/hctl -mindepth 1 -maxdepth 1 ! -name .codex ! -name .config -print -quit)"
  test -z "$(find /workspace -mindepth 1 -print -quit)"
  test ! -e /agent
  hctl integration verify github-mcp-server | grep -F "verified integration=github-mcp-server version=1.8.0" >/dev/null
  hctl integration inspect github-mcp-server | grep -F "required_environment=GITHUB_PERSONAL_ACCESS_TOKEN" >/dev/null
  hctl integration inspect github-mcp-server | grep -F "value=not-read" >/dev/null
  if grep -R "GITHUB_PERSONAL_ACCESS_TOKEN=" /home/hctl/.config/hctl/integrations; then exit 1; fi
  mkdir -p /tmp/codex-version
  CODEX_HOME=/tmp/codex-version codex --version | grep -F "'"$codex_version"'" >/dev/null
  deno --version >/dev/null
  test "$(python -c "import sys; print(sys.base_prefix)")" = /opt/hctl/runtimes/python
  uv --version >/dev/null
  test "$(go env GOROOT)" = /opt/hctl/runtimes/go
  test -s /etc/ssl/certs/ca-certificates.crt
  test -z "$(find /home/hctl/.codex -mindepth 1 -print -quit)"
  actual_libraries=$(for executable in deno uv python; do
    ldd "$(command -v "$executable")"
  done | awk "{ if (\$2 == \"=>\" && \$3 ~ /^\//) print \$1; else if (\$1 ~ /^\//) { count=split(\$1, parts, \"/\"); print parts[count] } }" | sort -u)
  expected_libraries=$(printf "%s\n" "$EXPECTED_SHARED_LIBRARIES" | tr , "\n")
  printf "actual shared libraries:\n%s\nexpected shared libraries:\n%s\n" "$actual_libraries" "$expected_libraries"
  test "$actual_libraries" = "$expected_libraries"
  for executable in hctl codex go; do
    ldd "$(command -v "$executable")" 2>&1 | grep -E "statically linked|not a dynamic executable" >/dev/null
  done
'

test "$(docker image inspect --format '{{index .Config.Labels "io.hctl.harness.version"}}' "$source_image")" = "$codex_version"
test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$source_image")" = "$hctl_version"
if docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$source_image" | grep -E '^(OPENAI_API_KEY|CODEX_API_KEY)='; then
  echo "Codex image contains a credential environment variable" >&2
  exit 1
fi

docker build --platform linux/amd64 \
  --build-arg "SOURCE_IMAGE=$source_image" \
  --tag "$direct_image" \
  --file "$repo_root/images/codex/acceptance/direct.Dockerfile" \
  "$repo_root"
if docker run --rm "$direct_image" >"$work/direct-entrypoint.out" 2>&1; then
  echo "direct image entrypoint unexpectedly completed" >&2
  exit 1
fi
grep -F "agent has no configured channels; use --input jsonl or add channels/discord.md" "$work/direct-entrypoint.out" >/dev/null
docker run --rm --network none --entrypoint /bin/sh "$direct_image" -c '
  set -eu
  test "$(id -u):$(id -g)" = "65532:65532"
  test -f /workspace/.codex/config.toml
  test -f /workspace/.hctl/apply/codex.json
  test ! -e /workspace/.claude
  test ! -e /workspace/CLAUDE.md
  test -z "$(find /home/hctl/.codex -mindepth 1 -print -quit)"
  hctl integration verify github-mcp-server >/dev/null
  test ! -e /workspace/.hctl/integrations
'

docker build --platform linux/amd64 \
  --build-arg "SOURCE_IMAGE=$source_image" \
  --build-arg "BASE_IMAGE=$base_image" \
  --tag "$staged_image" \
  --file "$repo_root/images/codex/acceptance/staged.Dockerfile" \
  "$repo_root"
if docker run --rm "$staged_image" >"$work/staged-entrypoint.out" 2>&1; then
  echo "staged image entrypoint unexpectedly completed" >&2
  exit 1
fi
grep -F "agent has no configured channels; use --input jsonl or add channels/discord.md" "$work/staged-entrypoint.out" >/dev/null
docker run --rm --entrypoint /bin/sh "$staged_image" -c '
  set -eu
  test "$(id -u):$(id -g)" = "65532:65532"
  test -x /opt/hctl/bin/agent-entrypoint
  test -x /opt/hctl/harness/bin/codex
  test ! -e /opt/hctl/runtimes
  test ! -e /opt/hctl/integrations
  test ! -e /home/hctl/.config/hctl/integrations
  grep -F "\"runtimes\": []" /opt/hctl/artifact.json >/dev/null
  test -f /workspace/.codex/config.toml
  test -s /etc/ssl/certs/ca-certificates.crt
  test -z "$(find /home/hctl/.codex -mindepth 1 -print -quit)"
  printf "%s\n" \
    "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\"}}" \
    "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}" \
    | timeout 10 /opt/hctl/bin/hctl mcp serve /opt/hctl/agents/agent --workspace /workspace --harness codex \
    | grep -F "\"name\":\"hctl-managed\"" >/dev/null
'

for runtime in deno python go; do
  context="$work/runtime-$runtime"
  mkdir -p "$context/agent"
  cp -R "$repo_root/images/codex/acceptance/fixtures/$runtime/." "$context/agent/"
  case "$runtime" in
    deno)
      cp "$repo_root/agents/maintainer/deno.json" "$repo_root/agents/maintainer/deno.lock" "$context/agent/"
      ;;
    python)
      cp "$repo_root/agents/maintainer/pyproject.toml" "$repo_root/agents/maintainer/uv.lock" "$context/agent/"
      ;;
    go)
      cp "$repo_root/agents/maintainer/go.mod" "$context/agent/"
      ;;
  esac

  runtime_image="hctl-codex-staged-$runtime:acceptance"
  docker build --platform linux/amd64 \
    --build-arg "SOURCE_IMAGE=$source_image" \
    --build-arg "BASE_IMAGE=$base_image" \
    --tag "$runtime_image" \
    --file "$repo_root/images/codex/acceptance/runtime-staged.Dockerfile" \
    "$context"

  if docker run --rm "$runtime_image" >"$work/staged-$runtime-entrypoint.out" 2>&1; then
    echo "$runtime staged image entrypoint unexpectedly completed" >&2
    exit 1
  fi
  grep -F "agent has no configured channels; use --input jsonl or add channels/discord.md" "$work/staged-$runtime-entrypoint.out" >/dev/null

  docker run --rm --entrypoint /bin/sh --env "EXPECTED_RUNTIME=$runtime" "$runtime_image" -c '
    set -eu
    test "$(id -u):$(id -g)" = "65532:65532"
    test -x /opt/hctl/bin/agent-entrypoint
    test -x /opt/hctl/harness/bin/codex
    test -f /workspace/.codex/config.toml
    test -s /etc/ssl/certs/ca-certificates.crt
    test -z "$(find /home/hctl/.codex -mindepth 1 -print -quit)"

    case "$EXPECTED_RUNTIME" in
      deno)
        test -x /opt/hctl/runtimes/deno/bin/deno
        test ! -e /opt/hctl/runtimes/python
        test ! -e /opt/hctl/runtimes/uv
        test ! -e /opt/hctl/runtimes/go
        test -z "$(find /workspace/.hctl/cache/tools -path "*/go/host" -type f -print -quit)"
        grep -F "\"deno\"" /opt/hctl/artifact.json >/dev/null
        for absent in python uv go-host; do
          if grep -F "\"$absent\"" /opt/hctl/artifact.json >/dev/null; then exit 1; fi
        done
        ;;
      python)
        test -n "$(find /opt/hctl/runtimes/python/bin -type f -perm -111 -print -quit)"
        test -x /opt/hctl/runtimes/uv/bin/uv
        test ! -e /opt/hctl/runtimes/deno
        test ! -e /opt/hctl/runtimes/go
        test -n "$(find /workspace/.hctl/cache/tools -path "*/python-venv/bin/python" -type f -perm -111 -print -quit)"
        test -z "$(find /workspace/.hctl/cache/tools -path "*/go/host" -type f -print -quit)"
        grep -F "\"python\"" /opt/hctl/artifact.json >/dev/null
        grep -F "\"uv\"" /opt/hctl/artifact.json >/dev/null
        for absent in deno go-host; do
          if grep -F "\"$absent\"" /opt/hctl/artifact.json >/dev/null; then exit 1; fi
        done
        ;;
      go)
        test ! -e /opt/hctl/runtimes
        go_host=$(find /workspace/.hctl/cache/tools -path "*/go/host" -type f -perm -111 -print -quit)
        test -n "$go_host"
        test -z "$(find /workspace/.hctl/cache/tools \( -name main.go -o -name go.mod -o -name go.sum \) -type f -print -quit)"
        grep -F "\"go-host\"" /opt/hctl/artifact.json >/dev/null
        for absent in deno python uv; do
          if grep -F "\"$absent\"" /opt/hctl/artifact.json >/dev/null; then exit 1; fi
        done
        ;;
    esac

    result=$(printf "%s\n" \
      "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\"}}" \
      "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}" \
      "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"runtime-probe\",\"arguments\":{}}}" \
      | timeout 10 /opt/hctl/bin/hctl mcp serve /opt/hctl/agents/agent --workspace /workspace --harness codex)
    printf "%s\n" "$result" | grep -F "\"name\":\"runtime-probe\"" >/dev/null
    printf "%s\n" "$result" | grep -F "\"runtime\":\"$EXPECTED_RUNTIME\"" >/dev/null
    printf "%s\n" "$result" | grep -F "\"isError\":false" >/dev/null
  '
done

printf '%s\n' "PASS Codex source, direct, tool-free, Deno-only, Python-only, and Go-only staged images"
