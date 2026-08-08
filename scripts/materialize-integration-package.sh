#!/bin/sh
set -eu

usage() {
  echo "usage: ./scripts/materialize-integration-package.sh --package DIR --platform OS-ARCH --output DIR" >&2
  exit 64
}

if [ "$#" -ne 6 ] || [ "$1" != "--package" ] || [ -z "$2" ] || [ "$3" != "--platform" ] || [ -z "$4" ] || [ "$5" != "--output" ] || [ -z "$6" ]; then
  usage
fi
package=$2
platform=$4
requested_output=$6

[ -f "$package/integration.json" ] && [ -f "$package/sources.tsv" ] || {
  echo "integration package metadata is incomplete" >&2
  exit 1
}
case "$platform" in
  '' | *[!a-z0-9-]* | -* | *-) echo "integration package platform is invalid" >&2; exit 1 ;;
esac
if [ -e "$requested_output" ]; then
  echo "integration package output already exists: $requested_output" >&2
  exit 1
fi
output_parent=$(CDPATH= cd -- "$(dirname -- "$requested_output")" && pwd -P) || {
  echo "integration package output parent must exist" >&2
  exit 1
}
output="$output_parent/$(basename -- "$requested_output")"

tab=$(printf '\t')
record=$(awk -F "$tab" -v platform="$platform" '$1 == platform { if (found) exit 2; print; found=1 } END { if (!found) exit 1 }' "$package/sources.tsv") || {
  echo "integration package source metadata is missing or ambiguous for $platform" >&2
  exit 1
}
IFS="$tab" read -r selected package_id url redirect_origin package_path archive_size archive_sha256 archive_layout executable_path executable_size executable_sha256 extra <<EOF
$record
EOF
[ "$selected" = "$platform" ] && [ -z "${extra:-}" ] || {
  echo "integration package source metadata is invalid" >&2
  exit 1
}
case "$package_id" in
  '' | *[!a-z0-9.-]* | [!a-z]* | *[.-] | *..* | *--* | *.-* | *-.*) echo "integration package id is invalid" >&2; exit 1 ;;
esac
case "$url" in
  https://*) ;;
  *) echo "integration package source must be HTTPS" >&2; exit 1 ;;
esac
case "$redirect_origin" in
  '' | *[!a-z0-9.-]* | .* | *. | *..*) echo "integration package redirect origin is invalid" >&2; exit 1 ;;
esac
case "$package_path" in
  payload/*.tar.gz)
    case "$package_path" in *../* | */../* | */..) echo "integration package payload path is invalid" >&2; exit 1 ;; esac
    ;;
  *) echo "integration package payload path is invalid" >&2; exit 1 ;;
esac
case "$executable_path" in
  '' | /* | -* | ./* | */./* | *../* | */../* | */.. | *//*) echo "integration package executable path is invalid" >&2; exit 1 ;;
esac

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    echo "a SHA-256 utility is required" >&2
    exit 1
  fi
}

stage=$(mktemp -d "$output_parent/.hctl-integration-package.XXXXXX")
cleanup() {
  rm -rf -- "$stage"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$stage/$(dirname -- "$package_path")"
cp "$package/integration.json" "$stage/integration.json"

if ! redirect_probe=$(curl --fail --silent --show-error --head --proto '=https' \
  --write-out '\nHCTL_STATUS:%{http_code}\n' "$url"); then
  echo "integration package $package_id source is unavailable" >&2
  exit 1
fi
redirect_status=$(printf '%s\n' "$redirect_probe" | awk -F: '$1 == "HCTL_STATUS" { status=$2; count++ } END { if (count != 1) exit 1; print status }') || {
  echo "integration package $package_id release redirect response is invalid" >&2
  exit 1
}
case "$redirect_status" in
  301 | 302 | 303 | 307 | 308) ;;
  *) echo "integration package $package_id release URL did not return one approved redirect" >&2; exit 1 ;;
esac
redirect_url=$(printf '%s\n' "$redirect_probe" | tr -d '\r' | awk '
  tolower($1) == "location:" {
    count++
    sub(/^[^:]*:[[:space:]]*/, "")
    location=$0
  }
  END { if (count != 1 || location == "") exit 1; print location }
') || {
  echo "integration package $package_id release redirect response is invalid" >&2
  exit 1
}
[ "${#redirect_url}" -le 4096 ] || {
  echo "integration package $package_id release redirect is too large" >&2
  exit 1
}
case "$redirect_url" in
  "https://$redirect_origin/"*) ;;
  *) echo "integration package $package_id download left the approved release-asset origin" >&2; exit 1 ;;
esac
if ! curl --fail --silent --show-error --proto '=https' \
  --output "$stage/$package_path" "$redirect_url"; then
  echo "integration package $package_id source is unavailable" >&2
  exit 1
fi

actual_size=$(wc -c <"$stage/$package_path" | tr -d ' ')
[ "$actual_size" = "$archive_size" ] || {
  echo "integration package $package_id archive size does not match the pin" >&2
  exit 1
}
actual_sha256=$(sha256_file "$stage/$package_path")
[ "$actual_sha256" = "$archive_sha256" ] || {
  echo "integration package $package_id archive SHA-256 does not match the pin" >&2
  exit 1
}

actual_layout=$(tar -tzf "$stage/$package_path" | paste -sd, -)
[ "$actual_layout" = "$archive_layout" ] || {
  echo "integration package $package_id archive layout changed" >&2
  exit 1
}
tar -xOzf "$stage/$package_path" -- "$executable_path" >"$stage/.executable"
actual_executable_size=$(wc -c <"$stage/.executable" | tr -d ' ')
[ "$actual_executable_size" = "$executable_size" ] || {
  echo "integration package $package_id executable identity or version does not match the pinned size" >&2
  exit 1
}
actual_executable_sha256=$(sha256_file "$stage/.executable")
[ "$actual_executable_sha256" = "$executable_sha256" ] || {
  echo "integration package $package_id executable identity or version does not match the pinned SHA-256" >&2
  exit 1
}
rm "$stage/.executable"
chmod 600 "$stage/integration.json" "$stage/$package_path"
mv "$stage" "$output"
trap - EXIT HUP INT TERM
printf '%s\n' "materialized integration package=$output platform=$platform"
