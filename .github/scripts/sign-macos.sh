#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

binary=$1
target=$2
snapshot=$3
if [[ "$target" != darwin_* || "$snapshot" == true ]]; then
  exit 0
fi

signing_dir=${VOX_MACOS_SIGNING_DIR:?}
sign_log="$signing_dir/$target.sign.log"
if ! codesign --force --sign "${VOX_MACOS_SIGNING_IDENTITY:?}" \
  --keychain "$signing_dir/signing.keychain-db" --options runtime --timestamp "$binary" > "$sign_log" 2>&1; then
  if grep -qi 'unable to build chain' "$sign_log"; then
    echo "macOS signing certificate chain failed for $target." >&2
  elif grep -qi 'timestamp' "$sign_log"; then
    echo "macOS signing timestamp failed for $target." >&2
  elif grep -Eqi 'errSecInternalComponent|interaction is not allowed' "$sign_log"; then
    echo "macOS signing key access failed for $target." >&2
  else
    echo "macOS signing failed for $target." >&2
  fi
  exit 1
fi
if ! codesign --verify --strict "$binary"; then
  echo "macOS signature verification failed for $target." >&2
  exit 1
fi

archive="$signing_dir/$target.zip"
result="$signing_dir/$target.json"
ditto -c -k --keepParent "$binary" "$archive"
if ! xcrun notarytool submit "$archive" --key "$signing_dir/notary.p8" \
  --key-id "${APPSTORE_CONNECT_KEY_ID:?}" --issuer "${APPSTORE_CONNECT_ISSUER_ID:?}" \
  --wait --timeout 15m --output-format json > "$result" ||
  ! jq -e '.status == "Accepted"' "$result" >/dev/null; then
  echo "macOS notarization failed for $target." >&2
  exit 1
fi
