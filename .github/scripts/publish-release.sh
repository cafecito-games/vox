#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

for name in APPLE_DEVELOPER_ID_P12_BASE64 APPLE_DEVELOPER_ID_P12_PASSWORD \
  APPSTORE_CONNECT_ISSUER_ID APPSTORE_CONNECT_KEY_ID APPSTORE_CONNECT_PRIVATE_KEY_BASE64; do
  if [[ -z "${!name:-}" ]]; then
    echo "::error::Missing release credential $name."
    exit 1
  fi
done

signing_dir=$(mktemp -d "${RUNNER_TEMP:?}/vox-notary.XXXXXX")
signing_keychain="$signing_dir/signing.keychain-db"
cleanup() {
  security delete-keychain "$signing_keychain" >/dev/null 2>&1 || true
  rm -rf -- "$signing_dir"
}
trap cleanup EXIT

certificate="$signing_dir/developer-id.p12"
if ! printf '%s' "$APPLE_DEVELOPER_ID_P12_BASE64" | base64 --decode > "$certificate" 2>/dev/null ||
   ! printf '%s' "$APPSTORE_CONNECT_PRIVATE_KEY_BASE64" | base64 --decode > "$signing_dir/notary.p8" 2>/dev/null; then
  echo '::error::A macOS signing credential is invalid Base64.'
  exit 1
fi
unset APPLE_DEVELOPER_ID_P12_BASE64 APPSTORE_CONNECT_PRIVATE_KEY_BASE64

keychain_password=$(openssl rand -hex 32)
if ! {
  security create-keychain -p "$keychain_password" "$signing_keychain" &&
  security set-keychain-settings -lut 3600 "$signing_keychain" &&
  security unlock-keychain -p "$keychain_password" "$signing_keychain" &&
  security import "$certificate" -P "$APPLE_DEVELOPER_ID_P12_PASSWORD" -f pkcs12 \
    -k "$signing_keychain" -T /usr/bin/codesign &&
  security set-key-partition-list -S apple-tool:,apple: -s -k "$keychain_password" "$signing_keychain"
} > "$signing_dir/keychain.log" 2>&1; then
  echo '::error::Could not import the Developer ID certificate into the temporary keychain.'
  exit 1
fi
unset APPLE_DEVELOPER_ID_P12_PASSWORD keychain_password

# codesign uses the user search list to find intermediate certificates even
# when --keychain restricts which signing identity it can select.
if ! keychain_search=$(security list-keychains -d user 2>> "$signing_dir/keychain.log"); then
  echo '::error::Could not read the runner keychain search list.'
  exit 1
fi
keychains=("$signing_keychain")
keychain_entry='^[[:space:]]*"([^"]+)"[[:space:]]*$'
while IFS= read -r entry; do
  if [[ -z "$entry" ]]; then
    continue
  fi
  if [[ ! "$entry" =~ $keychain_entry ]]; then
    echo '::error::Could not parse the runner keychain search list.'
    exit 1
  fi
  if [[ "${BASH_REMATCH[1]}" != "$signing_keychain" ]]; then
    keychains+=("${BASH_REMATCH[1]}")
  fi
done <<< "$keychain_search"
if ! security list-keychains -d user -s "${keychains[@]}" >> "$signing_dir/keychain.log" 2>&1; then
  echo '::error::Could not add the temporary signing keychain to the search list.'
  exit 1
fi

identity=$(security find-identity -v -p codesigning "$signing_keychain" 2>> "$signing_dir/keychain.log" |
  awk '/"Developer ID Application:/ {print $2}')
if [[ ! "$identity" =~ ^[[:xdigit:]]{40}$ ]]; then
  echo '::error::The signing keychain must contain one valid Developer ID Application identity.'
  exit 1
fi
export VOX_MACOS_SIGNING_DIR="$signing_dir"
export VOX_MACOS_SIGNING_IDENTITY="$identity"

# The signing tool can see private keys and service responses. Keep its raw
# output off public logs and out of runner summaries.
unset GITHUB_STEP_SUMMARY
if ! goreleaser release --clean > "$signing_dir/release.log" 2>&1; then
  if grep -Fq 'macOS signing certificate chain failed' "$signing_dir/release.log"; then
    echo '::error::codesign could not build the Developer ID certificate chain.'
  elif grep -Fq 'macOS signing timestamp failed' "$signing_dir/release.log"; then
    echo '::error::codesign could not obtain an Apple timestamp.'
  elif grep -Fq 'macOS signing key access failed' "$signing_dir/release.log"; then
    echo '::error::codesign could not use the Developer ID private key on the runner.'
  elif grep -Fq 'macOS signing failed' "$signing_dir/release.log"; then
    echo '::error::codesign could not sign a macOS binary.'
  elif grep -Fq 'macOS signature verification failed' "$signing_dir/release.log"; then
    echo '::error::A signed macOS binary failed strict signature verification.'
  elif grep -Fq 'macOS notarization failed' "$signing_dir/release.log"; then
    echo '::error::Apple notarization did not return Accepted. Check the notarization status before retrying.'
  else
    echo '::error::GoReleaser failed before completing the release.'
  fi
  exit 1
fi
echo 'Published release with signed and notarized macOS binaries.'
