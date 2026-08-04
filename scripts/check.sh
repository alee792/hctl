#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tools_root="$repo_root/.tools"

if [ ! -x "$tools_root/go/bin/go" ] || [ ! -x "$tools_root/bin/golangci-lint" ]; then
  echo "development tools are missing; run ./scripts/bootstrap-tools.sh" >&2
  exit 1
fi

export PATH="$tools_root/go/bin:$tools_root/bin:$PATH"
export GOCACHE="$tools_root/cache/build"
export GOMODCACHE="$tools_root/cache/mod"

cd "$repo_root"

unformatted=$(find cmd internal -type f -name '*.go' -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
  echo "gofmt is required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

unimported=$(find cmd internal -type f -name '*.go' -exec goimports -l {} +)
if [ -n "$unimported" ]; then
  echo "goimports is required for:" >&2
  echo "$unimported" >&2
  exit 1
fi

go test ./...
go vet ./...
golangci-lint run
govulncheck ./...
