#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tools_root="$repo_root/.tools"

if [ ! -x "$tools_root/go/bin/go" ]; then
  echo "repository-local Go is missing; run ./scripts/bootstrap-tools.sh" >&2
  exit 1
fi
if [ ! -x "$tools_root/bin/deno" ]; then
  echo "repository-local Deno is missing" >&2
  exit 1
fi
if ! command -v uv >/dev/null 2>&1; then
  echo "uv is required on PATH" >&2
  exit 1
fi

export PATH="$tools_root/go/bin:$tools_root/bin:$PATH"
export GOCACHE="$tools_root/cache/build"
export GOMODCACHE="$tools_root/cache/mod"

cd "$repo_root"

unformatted=$(gofmt -l spikes/polyglot-tools/probe spikes/polyglot-tools/fixture/tools)
if [ -n "$unformatted" ]; then
  echo "gofmt is required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

deno fmt --check \
  spikes/polyglot-tools/host/typescript.ts \
  spikes/polyglot-tools/fixture/tools/repeat.ts \
  spikes/polyglot-tools/cases/duplicate/tools/same.ts \
  spikes/polyglot-tools/cases/invalid-signature/tools/bad.ts \
  spikes/polyglot-tools/cases/timeout/tools/slow.ts

uv run --locked --project spikes/polyglot-tools/fixture \
  python -m compileall -q \
  spikes/polyglot-tools/host/python.py \
  spikes/polyglot-tools/fixture/tools/add.py \
  spikes/polyglot-tools/cases/duplicate/tools/same.py \
  spikes/polyglot-tools/cases/invalid-output/tools/bad.py

go run ./spikes/polyglot-tools/probe
