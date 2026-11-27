#!/usr/bin/env bash
set -euo pipefail

version=${VOX_VERSION:-}
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo '::error::Use a Vox release tag as the action ref or set vox-version explicitly.'
  exit 1
fi

case "${RUNNER_OS:-}" in
  Linux) os=Linux ;;
  macOS) os=Darwin ;;
  *) echo '::error::Vox setup supports Linux and macOS runners.'; exit 1 ;;
esac
case "${RUNNER_ARCH:-}" in
  X64) arch=x86_64 ;;
  ARM64) arch=arm64 ;;
  *) echo '::error::Vox setup supports X64 and ARM64 runners.'; exit 1 ;;
esac

work_dir=$(mktemp -d "$RUNNER_TEMP/vox-download.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT
archive_name="vox_${version#v}_${os}_${arch}.tar.gz"
archive="$work_dir/$archive_name"
release_url="https://github.com/cafecito-games/vox/releases/download/$version"
curl_options=(--fail --silent --show-error --location --proto '=https' --proto-redir '=https'
  --retry 2 --connect-timeout 10 --max-time 90)
curl "${curl_options[@]}" --output "$work_dir/checksums.txt" "$release_url/checksums.txt"
curl "${curl_options[@]}" --output "$archive" "$release_url/$archive_name"

expected=$(awk -v name="$archive_name" '$2 == name || $2 == "*" name { print tolower($1) }' "$work_dir/checksums.txt")
if [[ ! $expected =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo '::error::Vox release checksums must contain one SHA-256 entry for the runner archive.'
  exit 1
fi
if command -v sha256sum >/dev/null; then
  checksum=$(sha256sum "$archive")
else
  checksum=$(shasum -a 256 "$archive")
fi
if [[ ${checksum%% *} != "$expected" ]]; then
  echo '::error::Vox release archive failed SHA-256 verification.'
  exit 1
fi

tar -xzf "$archive" -C "$work_dir" vox
bin_dir=$(mktemp -d "$RUNNER_TEMP/vox-bin.XXXXXX")
install -m 0755 "$work_dir/vox" "$bin_dir/vox"
printf '%s\n' "$bin_dir" >> "$GITHUB_PATH"
printf 'path=%s\n' "$bin_dir/vox" >> "$GITHUB_OUTPUT"
