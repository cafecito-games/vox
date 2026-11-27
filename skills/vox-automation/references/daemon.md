# Vox daemon and interactions

Launch `vox serve` as a persistent child process with separate stdin, stdout, and stderr. The token belongs in the runner's secret configuration.

```sh
vox serve --server "$guild_id" --channel "$channel_id" --events messages --no-include-self
```

Wait for an event with `"event":"ready"`. Write one JSON object per line to stdin and flush each line. Use a unique `req_id` for each request and match it to an `"event":"response"` line. Check the response's `error` field. Discord events can arrive between responses. Read stdout continuously, use strings for Discord IDs, and close stdin or terminate the child when the task ends.

```sh
vox schema --json | jq '.serve_actions[] | select(.name == "reply")'
```

```jsonl
{"action":"message_list","channel_id":"123456789012345678","limit":5,"req_id":"read-1"}
{"action":"reply","channel_id":"123456789012345678","message_id":"234567890123456789","content":"Thanks for the update","req_id":"reply-1"}
```

Keep the daemon's stdin within the trusted runner. CLI profiles govern entry to `serve`, and its server/channel flags filter events. Enforce authorization for individual actions and targets in the runner.

For live output, send `stream_start` with `channel_id`, wait for the returned `stream_id`, and send `stream_chunk` requests with that ID and each text delta in `content`. End with `stream_end`. Vox batches message edits and splits overflow across Discord messages.

## Slash commands and components

Pass `--slash-commands commands.json` to sync a JSON array of command definitions. Each entry has a `name`, `description`, and optional `params`. Params have `name`, `description`, and optional `type` and `required`. Supported types are `string`, `integer`, `number`, and `boolean`. Vox bulk-replaces the application's guild commands in every guild returned at startup. Check the existing command sets and use a sandbox bot when testing. The event filter leaves command registration scope unchanged. Check the `slash_commands_synced` event and registration errors.

- Handle `slash_command`, `component_interaction`, and `modal_submit` using the event's `interaction_token` in the same session. Vox defers slash commands and modal submissions. Use `interaction_edit` for the original deferred response or `interaction_followup` for another message.
- An unacknowledged button or select can open a modal with `modal_send` and fields such as `{"label":"Summary","custom_id":"summary","style":"short"}`. Dispatch promptly because Vox auto-defers components after 2.5 seconds. Use `interaction_respond` for an immediate message before acknowledgement. If you need multiple interaction actions, inspect token retention behavior and request `keep_token` where supported.
- `vox interact modal` posts a trigger button. Opening and handling a modal requires a running interaction handler.

## Stateful workflows and dashboards

Start workflows that must handle clicks with `workflow_start` in the owning daemon. Supply a `channel_id`, `user_id`, and a `workflow` object with a `workflow_id` and ordered `steps`. Each step has a `step_id` and `step_type`. A `next` string selects a next step, and a `next` object can map a selected value to a step ID. Listen for `workflow_step_completed`, `workflow_finished`, and `workflow_timeout`. Use `workflow_cancel` to end a session early.

Use `dashboard_create` with a `channel_id` and a `dashboard` object. The object has `pages` and can include a `dashboard_id` and `refresh_interval`. Every page uses either embed layout or Components v2 layout. V2 pages use `"layout":"v2"` and a nonempty `blocks` array. For example, `{"dashboard_id":"status","pages":[{"layout":"v2","blocks":[{"type":"text","content":"# Release status"}]}]}` renders a single page. Use the returned ID for `dashboard_update` and `dashboard_delete`.

Workflow and dashboard handler state belongs to the process that created it. Keep that daemon running for interactive or updating surfaces. The one-shot `interact` commands can publish a Discord message, and the process exits after the operation.
