#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tools_root="$repo_root/.tools"
go_version=1.26.5

case "$(uname -s)-$(uname -m)" in
  Darwin-x86_64)
    go_platform=darwin-amd64
    go_sha256=6231d8d3b8f5552ec6cbf6d685bdd5482e1e703214b120e89b3bf0d7bf1ef725
    ;;
  Darwin-arm64)
    go_platform=darwin-arm64
    go_sha256=efb87ff28af9a188d0536ef5d42e63dd52ba8263cd7344a993cc48dd11dedb6a
    ;;
  Linux-x86_64)
    go_platform=linux-amd64
    go_sha256=5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053
    ;;
  Linux-aarch64 | Linux-arm64)
    go_platform=linux-arm64
    go_sha256=fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49
    ;;
  *)
    echo "unsupported development platform: $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

go_root="$tools_root/go"
go_bin="$go_root/bin/go"
expected_version="go version go${go_version} "

if [ -x "$go_bin" ]; then
  case "$($go_bin version)" in
    "$expected_version"*) ;;
    *)
      echo "$go_root contains a different Go version; move it aside and retry" >&2
      exit 1
      ;;
  esac
else
  if [ -e "$go_root" ]; then
    echo "$go_root exists but is not a usable Go ${go_version} installation" >&2
    exit 1
  fi

  mkdir -p "$tools_root/downloads"
  archive="go${go_version}.${go_platform}.tar.gz"
  archive_path="$tools_root/downloads/$archive"
  if [ ! -f "$archive_path" ]; then
    curl -fL "https://go.dev/dl/$archive" -o "$archive_path"
  fi

  if command -v shasum >/dev/null 2>&1; then
    actual_sha256=$(shasum -a 256 "$archive_path" | awk '{print $1}')
  elif command -v sha256sum >/dev/null 2>&1; then
    actual_sha256=$(sha256sum "$archive_path" | awk '{print $1}')
  else
    echo "a SHA-256 utility (shasum or sha256sum) is required" >&2
    exit 1
  fi
  if [ "$actual_sha256" != "$go_sha256" ]; then
    echo "checksum mismatch for $archive_path" >&2
    exit 1
  fi
  tar -xzf "$archive_path" -C "$tools_root"
fi

mkdir -p "$tools_root/bin" "$tools_root/cache/build" "$tools_root/cache/mod"
export GOBIN="$tools_root/bin"
export GOCACHE="$tools_root/cache/build"
export GOMODCACHE="$tools_root/cache/mod"

install_tool() {
  binary=$1
  package=$2
  module=$3
  version=$4

  if [ -x "$tools_root/bin/$binary" ] &&
    "$go_bin" version -m "$tools_root/bin/$binary" |
      awk -v module="$module" -v version="$version" '$1 == "mod" && $2 == module && $3 == version { found = 1 } END { exit !found }'; then
    return
  fi
  "$go_bin" install "$package@$version"
}

install_tool gopls golang.org/x/tools/gopls golang.org/x/tools/gopls v0.23.0
install_tool golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint github.com/golangci/golangci-lint/v2 v2.12.2
install_tool goimports golang.org/x/tools/cmd/goimports golang.org/x/tools v0.48.0
install_tool govulncheck golang.org/x/vuln/cmd/govulncheck golang.org/x/vuln v1.6.0
install_tool actionlint github.com/rhysd/actionlint/cmd/actionlint github.com/rhysd/actionlint v1.7.12

"$go_bin" clean -cache -modcache

echo "Installed repository-local Go tools in $tools_root"
echo "Add them to this shell with:"
echo "  export PATH=\"$tools_root/go/bin:$tools_root/bin:\$PATH\""
