---
name: vox-ci
description: Set up Vox for CI/CD Discord notifications, add its GitHub Actions, or replace webhook delivery scripts in build, release, and deployment workflows.
---

# Vox for CI/CD

Inspect the project's workflow and any existing notifier before editing. Identify which jobs trigger the notification, where they get their result and payload, which secret selects the destination, and whether delivery failures should affect the run.

## Choose the integration

| Need | Use |
| --- | --- |
| Post a message or status embed in GitHub Actions | `cafecito-games/vox/actions/notify` |
| Send an existing Discord message payload | The same action with `payload-file` |
| Attach files, issue other Vox commands, or reuse Vox for several steps | `cafecito-games/vox/actions/setup` followed by CLI commands |
| Send a webhook from another CI system | Install a pinned, checksum-verified Vox release and run `vox webhook post` |

Both GitHub Actions support Linux and macOS on X64 or ARM64 and do not require `jq`. `actions/setup` caches verified binaries under `$RUNNER_TOOL_CACHE/vox/<version>/<arch>/vox` (re-verifying the SHA-256 digest on reuse), honours a pre-seeded `VOX_BIN` or `vox` on `PATH` matching the requested version, and exposes `path` and `cache-hit` outputs. Verify that the chosen action ref contains the action and the selected Vox release includes `webhook post`. A release tag selects the matching binary by default. Set `vox-version` explicitly when pinning the action to a commit SHA. If the needed release is pending, keep the existing delivery step active and report the dependency.

## Configure a notification

Keep the channel webhook URL in a CI secret and pass it as `DISCORD_WEBHOOK_URL`. The URL determines the destination, so a bot token is unnecessary. Use the project's existing secret name when migrating.

For a workflow with a `build` job, add a dependent job when you need a notification on unsuccessful builds too. Replace `vX.Y.Z` with a compatible release.

```yaml
  notify:
    needs: build
    if: always()
    runs-on: ubuntu-latest
    permissions: {}
    steps:
      - id: discord
        uses: cafecito-games/vox/actions/notify@vX.Y.Z
        env:
          DISCORD_WEBHOOK_URL: ${{ secrets.DISCORD_WEBHOOK_URL }}
        with:
          title: Build · ${{ needs.build.result }}
          status: ${{ needs.build.result }}
          message: ${{ github.workflow }} run #${{ github.run_number }}
```

Use `message` alone for text or pair it with `title`, `status`, `color`, or `fields-json` for an embed. `status` maps `success`, `failure`, `warning`, `cancelled`, `skipped`, and `unknown` to distinct palette colors, while `color` accepts an arbitrary hex RGB value (mutually exclusive with `status`). The action links to the current run by default. Use `thread-id` when posting to an existing thread. Set `truncate: true` to clip overlong embeds to Discord's limits with an ellipsis and `escape-fields: true` to escape untrusted Discord markdown in field names and values. Confirmed deliveries expose `steps.discord.outputs.message-id`, `steps.discord.outputs.message-ids` (JSON array), and `steps.discord.outputs.delivered`.

For a JSON report or multi-message alert batch, generate or download it into the notification runner's workspace and pass `payload-file` on its own as the message source. `payload-file` accepts a single Discord webhook JSON object, a JSON array of objects, or a JSONL stream and posts them sequentially over one rate-limit-aware client. If a file attachment is required, use the setup action and then run `vox webhook post --payload-file ./alert.json --file ./report.txt --json` with the webhook secret in that step. Mentions are suppressed by default. An explicit `allowed_mentions` in a JSON payload controls which mentions are allowed.

For bot-authenticated commands after setup, inject `DISCORD_BOT_TOKEN` in the step that needs it. Use `vox schema --json` to check which commands are available in the selected release.

## Replace an existing delivery script

- Preserve job dependencies, triggers, conditions, and destination. When notifications are optional or best-effort, pass `required: false` and `fail-on-delivery-error: false` to `actions/notify` (or `--best-effort` to `vox webhook post`). Prefer these over `continue-on-error: true`, especially when a step also owns the job's exit code.
- Replace simple message rendering with action inputs. Use `--truncate` and `--escape-fields` (or the matching action inputs) instead of re-implementing Discord embed length caps or markdown escapers. Remove the old HTTP delivery path once the new workflow covers it.
- Scope secrets to trusted jobs. Keep webhook values out of commands and logs. Keep dynamic values in action inputs or environment variables, and use `toJSON` when embedding a GitHub expression as a JSON string.

Validate the workflow with `actionlint` when available. Exercise relevant success and failure cases with a mock or an authorized test channel. Verify the expected destination and that notification delivery preserves the workflow's failure behavior.
