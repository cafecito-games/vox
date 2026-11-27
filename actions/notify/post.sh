#!/usr/bin/env bash
set -euo pipefail

parse_bool() {
  local name=$1
  local value=$2
  case "$value" in
    true|false) printf '%s\n' "$value" ;;
    *) echo "::error::Input $name must be true or false."; exit 1 ;;
  esac
}

required=$(parse_bool "required" "${VOX_NOTIFY_REQUIRED:-true}")
fail_on_delivery_error=$(parse_bool "fail-on-delivery-error" "${VOX_NOTIFY_FAIL_ON_DELIVERY_ERROR:-true}")
truncate=$(parse_bool "truncate" "${VOX_NOTIFY_TRUNCATE:-false}")
escape_fields=$(parse_bool "escape-fields" "${VOX_NOTIFY_ESCAPE_FIELDS:-false}")

message=${VOX_NOTIFY_MESSAGE:-}
title=${VOX_NOTIFY_TITLE:-}
status=${VOX_NOTIFY_STATUS:-}
color_input=${VOX_NOTIFY_COLOR:-}
url_input=${VOX_NOTIFY_URL:-}
fields=${VOX_NOTIFY_FIELDS:-}
payload=${VOX_NOTIFY_PAYLOAD:-}
thread=${VOX_NOTIFY_THREAD:-}

args=(webhook post --output github-output)
if [[ -n "$thread" ]]; then
  args+=(--thread "$thread")
fi
if [[ "$fail_on_delivery_error" == "false" ]]; then
  args+=(--best-effort)
fi
if [[ "$truncate" == "true" ]]; then
  args+=(--truncate)
fi
if [[ "$escape_fields" == "true" ]]; then
  args+=(--escape-fields)
fi

if [[ -n "$payload" ]]; then
  if [[ -n "$message$title$status$color_input$url_input$fields" ]]; then
    echo '::error::Use either payload-file or message and embed inputs.'
    exit 1
  fi
  args+=(--payload-file "$payload")
else
  if [[ -z "$message$title$fields" ]]; then
    echo '::error::Provide a message, embed title, embed fields, or payload-file.'
    exit 1
  fi
  if [[ -n "$status" && -n "$color_input" ]]; then
    echo '::error::Use either status or color, not both.'
    exit 1
  fi
  url=$url_input
  if [[ -z "$url" && -n "${GITHUB_RUN_ID:-}" ]]; then
    url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"
  fi
  if [[ -n "$title$status$color_input$url_input$fields" ]]; then
    color=$color_input
    if [[ -z "$color" ]]; then
      case "$status" in
        success) color=2ECC71 ;;
        failure) color=E74C3C ;;
        warning) color=F39C12 ;;
        cancelled) color=607D8B ;;
        skipped|unknown) color=95A5A6 ;;
        '') color= ;;
        *) echo '::error::Unrecognized notification status.'; exit 1 ;;
      esac
    fi
    embed_title=$title
    if [[ -z "$embed_title" && -n "$url" ]]; then
      embed_title='GitHub Actions'
    fi
    args+=(--embed-title "$embed_title" --embed-desc "$message" --embed-url "$url")
    if [[ -n "$color" ]]; then
      args+=(--embed-color "$color")
    fi
    if [[ -n "$fields" ]]; then
      args+=(--embed-fields-json "$fields")
    fi
  else
    plain_message=$message
    if [[ -n "$url" ]]; then
      printf -v plain_message '%s\n\n[Open Actions run](%s)' "$plain_message" "$url"
    fi
    args+=(-- "$plain_message")
  fi
fi

trimmed_url=$(printf '%s' "${DISCORD_WEBHOOK_URL:-}" | tr -d '[:space:]')
if [[ -z "$trimmed_url" ]]; then
  if [[ "$required" == "false" ]]; then
    echo '::warning::DISCORD_WEBHOOK_URL is not configured; skipping Discord notification.'
    printf 'message-id=\nmessage-ids=[]\ndelivered=false\n' >> "$GITHUB_OUTPUT"
    exit 0
  fi
  echo '::error::Set DISCORD_WEBHOOK_URL to the Discord webhook secret.'
  exit 1
fi

stderr_file=$(mktemp "${RUNNER_TEMP:-/tmp}/vox-notify-stderr.XXXXXX")
trap 'rm -f -- "$stderr_file"' EXIT

set +e
response=$("$VOX_BIN" "${args[@]}" 2>"$stderr_file")
exit_code=$?
set -e

if [[ $exit_code -ne 0 ]]; then
  cat "$stderr_file" >&2
  exit "$exit_code"
fi

printf '%s\n' "$response" >> "$GITHUB_OUTPUT"

if printf '%s\n' "$response" | grep -qx 'delivered=false'; then
  warning_text=$(tr '\n' ' ' < "$stderr_file" | sed 's/^warning: //; s/[[:space:]]*$//')
  if [[ -z "$warning_text" ]]; then
    warning_text='Discord webhook delivery failed.'
  fi
  echo "::warning::$warning_text"
elif [[ -s "$stderr_file" ]]; then
  cat "$stderr_file" >&2
fi
