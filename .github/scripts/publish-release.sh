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
trap 'rm -rf -- "$signing_dir"' EXIT

export VOX_MACOS_CERTIFICATE_FILE="$signing_dir/developer-id.p12"
export VOX_MACOS_NOTARY_KEY_FILE="$signing_dir/notary.p8"
if ! printf '%s' "$APPLE_DEVELOPER_ID_P12_BASE64" | base64 --decode > "$VOX_MACOS_CERTIFICATE_FILE" 2>/dev/null ||
   ! printf '%s' "$APPSTORE_CONNECT_PRIVATE_KEY_BASE64" | base64 --decode > "$VOX_MACOS_NOTARY_KEY_FILE" 2>/dev/null; then
  echo '::error::A macOS signing credential is invalid Base64.'
  exit 1
fi
unset APPLE_DEVELOPER_ID_P12_BASE64 APPSTORE_CONNECT_PRIVATE_KEY_BASE64

# The signing tool can see private keys and service responses. Keep its raw
# output off public logs and out of runner summaries.
unset GITHUB_STEP_SUMMARY
if ! goreleaser release --clean > "$signing_dir/release.log" 2>&1; then
  if grep -qi 'timeout waiting for notarize' "$signing_dir/release.log"; then
    echo '::error::Apple notarization timed out. Retry the same release from the same commit.'
  else
    echo '::error::GoReleaser failed. Check the release credentials and Apple notarization status before retrying.'
  fi
  exit 1
fi
echo 'Published release with signed and notarized macOS binaries.'
