#!/usr/bin/env bash
set -euo pipefail

args=(webhook post --json)
if [[ -n "$VOX_NOTIFY_THREAD" ]]; then
  args+=(--thread "$VOX_NOTIFY_THREAD")
fi

if [[ -n "$VOX_NOTIFY_PAYLOAD" ]]; then
  if [[ -n "$VOX_NOTIFY_MESSAGE$VOX_NOTIFY_TITLE$VOX_NOTIFY_STATUS$VOX_NOTIFY_URL$VOX_NOTIFY_FIELDS" ]]; then
    echo '::error::Use either payload-file or message and embed inputs.'
    exit 1
  fi
  args+=(--payload-file "$VOX_NOTIFY_PAYLOAD")
else
  if [[ -z "$VOX_NOTIFY_MESSAGE$VOX_NOTIFY_TITLE$VOX_NOTIFY_FIELDS" ]]; then
    echo '::error::Provide a message, embed title, embed fields, or payload-file.'
    exit 1
  fi
  url=$VOX_NOTIFY_URL
  if [[ -z "$url" && -n "${GITHUB_RUN_ID:-}" ]]; then
    url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"
  fi
  if [[ -n "$VOX_NOTIFY_TITLE$VOX_NOTIFY_STATUS$VOX_NOTIFY_URL$VOX_NOTIFY_FIELDS" ]]; then
    case "$VOX_NOTIFY_STATUS" in
      success) color=2ECC71 ;;
      failure) color=E74C3C ;;
      warning|cancelled) color=F39C12 ;;
      skipped|unknown) color=95A5A6 ;;
      '') color= ;;
      *) echo '::error::Unrecognized notification status.'; exit 1 ;;
    esac
    title=$VOX_NOTIFY_TITLE
    if [[ -z "$title" && -n "$url" ]]; then
      title='GitHub Actions'
    fi
    args+=(--embed-title "$title" --embed-desc "$VOX_NOTIFY_MESSAGE" --embed-url "$url")
    if [[ -n "$color" ]]; then
      args+=(--embed-color "$color")
    fi
    if [[ -n "$VOX_NOTIFY_FIELDS" ]]; then
      args+=(--embed-fields-json "$VOX_NOTIFY_FIELDS")
    fi
  else
    message=$VOX_NOTIFY_MESSAGE
    if [[ -n "$url" ]]; then
      printf -v message '%s\n\n[Open Actions run](%s)' "$message" "$url"
    fi
    args+=(-- "$message")
  fi
fi

if ! command -v jq >/dev/null; then
  echo '::error::Vox notify requires jq on the runner.'
  exit 1
fi
response=$("$VOX_BIN" "${args[@]}")
message_id=$(jq -er '.id | strings | select(test("^[0-9]+$"))' <<< "$response")
printf 'message-id=%s\n' "$message_id" >> "$GITHUB_OUTPUT"
