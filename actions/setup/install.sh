#!/usr/bin/env bash
set -euo pipefail

version=${VOX_VERSION:-}
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo '::error::Use a Vox release tag as the action ref or set vox-version explicitly.'
  exit 1
fi
version_number=${version#v}

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

sha256_file() {
  local checksum
  if command -v sha256sum >/dev/null 2>&1; then
    checksum=$(sha256sum "$1")
  else
    checksum=$(shasum -a 256 "$1")
  fi
  printf '%s\n' "${checksum%% *}" | tr '[:upper:]' '[:lower:]'
}

version_matches() {
  local bin=$1
  local expected=$2
  [[ -n "$bin" && -f "$bin" && -x "$bin" ]] || return 1
  local reported
  reported=$("$bin" --version 2>/dev/null || true)
  if [[ $reported =~ ^vox\ version\ v?([^[:space:]]+)([[:space:]]|$) ]]; then
    [[ ${BASH_REMATCH[1]} == "$expected" ]]
    return
  fi
  return 1
}

emit_outputs() {
  local bin_path=$1
  local cache_hit=$2
  local add_to_path=$3
  if [[ "$add_to_path" == "true" ]]; then
    printf '%s\n' "$(dirname "$bin_path")" >> "$GITHUB_PATH"
  fi
  printf 'path=%s\n' "$bin_path" >> "$GITHUB_OUTPUT"
  printf 'cache-hit=%s\n' "$cache_hit" >> "$GITHUB_OUTPUT"
}

if [[ -n "${VOX_BIN:-}" ]] && version_matches "$VOX_BIN" "$version_number"; then
  emit_outputs "$VOX_BIN" "true" "true"
  exit 0
fi

existing_vox=$(command -v vox 2>/dev/null || true)
if [[ -n "$existing_vox" ]] && version_matches "$existing_vox" "$version_number"; then
  emit_outputs "$existing_vox" "true" "false"
  exit 0
fi

cache_dir=""
if [[ -n "${RUNNER_TOOL_CACHE:-}" ]]; then
  cache_dir="$RUNNER_TOOL_CACHE/vox/$version_number/$arch"
  cached_bin="$cache_dir/vox"
  cached_sha_file="$cache_dir/vox.sha256"
  if [[ -f "$cached_bin" ]]; then
    if [[ -x "$cached_bin" && -f "$cached_sha_file" ]]; then
      expected_bin_sha=$(awk 'NR == 1 { print tolower($1) }' "$cached_sha_file")
      if [[ $expected_bin_sha =~ ^[0-9a-f]{64}$ ]]; then
        actual_bin_sha=$(sha256_file "$cached_bin")
        if [[ "$actual_bin_sha" == "$expected_bin_sha" ]] && version_matches "$cached_bin" "$version_number"; then
          emit_outputs "$cached_bin" "true" "true"
          exit 0
        fi
      fi
    fi
    echo "::warning::Cached Vox binary at $cached_bin failed verification; downloading a fresh copy."
  fi
fi

work_dir=$(mktemp -d "${RUNNER_TEMP:?RUNNER_TEMP must be set}/vox-download.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT
archive_name="vox_${version_number}_${os}_${arch}.tar.gz"
archive="$work_dir/$archive_name"
release_url="https://github.com/cafecito-games/vox/releases/download/$version"
curl_options=(--fail --silent --show-error --location --proto '=https' --proto-redir '=https'
  --retry 2 --connect-timeout 10 --max-time 90)
curl "${curl_options[@]}" --output "$work_dir/checksums.txt" "$release_url/checksums.txt"
curl "${curl_options[@]}" --output "$archive" "$release_url/$archive_name"

expected=$(awk -v name="$archive_name" '$2 == name || $2 == "*" name { print tolower($1) }' "$work_dir/checksums.txt")
if [[ ! $expected =~ ^[0-9a-f]{64}$ ]]; then
  echo '::error::Vox release checksums must contain one SHA-256 entry for the runner archive.'
  exit 1
fi
actual_archive_sha=$(sha256_file "$archive")
if [[ "$actual_archive_sha" != "$expected" ]]; then
  echo '::error::Vox release archive failed SHA-256 verification.'
  exit 1
fi

tar -xzf "$archive" -C "$work_dir" vox
extracted_sha=$(sha256_file "$work_dir/vox")

installed_bin=""
if [[ -n "$cache_dir" ]] && mkdir -p "$cache_dir" 2>/dev/null; then
  tmp_bin=$(mktemp "$cache_dir/.vox.XXXXXX" 2>/dev/null || true)
  tmp_sha=$(mktemp "$cache_dir/.vox.sha256.XXXXXX" 2>/dev/null || true)
  if [[ -n "$tmp_bin" && -n "$tmp_sha" ]]; then
    install -m 0755 "$work_dir/vox" "$tmp_bin"
    printf '%s  vox\n' "$extracted_sha" > "$tmp_sha"
    chmod 0644 "$tmp_sha"
    mv -f "$tmp_bin" "$cache_dir/vox"
    mv -f "$tmp_sha" "$cache_dir/vox.sha256"
    installed_bin="$cache_dir/vox"
  else
    rm -f -- "${tmp_bin:-}" "${tmp_sha:-}"
  fi
fi

if [[ -z "$installed_bin" ]]; then
  bin_dir=$(mktemp -d "$RUNNER_TEMP/vox-bin.XXXXXX")
  install -m 0755 "$work_dir/vox" "$bin_dir/vox"
  installed_bin="$bin_dir/vox"
fi

emit_outputs "$installed_bin" "false" "true"
