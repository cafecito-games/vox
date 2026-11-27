---
name: vox-admin
description: Provision or configure Vox Discord bots and administer Discord servers with Vox, including roles, channel permissions, members, AutoMod, onboarding, and server-as-code changes.
---

# Vox administration

Inspect the relevant command with `vox <command> --help` or `vox schema --json`. Start from the supplied bot credential and config directory. Use the exact guild, channel, member, and role IDs when planning writes.

- For creation and installation handoffs, application settings, bot profiles, and install permissions, read [Bot provisioning](references/bot-provisioning.md).
- For server structure and moderation, use the workflows below.

## Server structure

- See current state with `server info`, `channel list --server`, `role list`, and `server onboarding show`. Channel results reflect the bot's visibility.
- For a focused change, use `server edit`, `channel create` or `channel edit`, `role create` or `role edit`, `role assign` or `role remove`, and `server onboarding edit`. `role create --permissions` takes a decimal bitfield. Server specs can express role permissions by name.
- `channel set-permissions` replaces the specified role or member's channel overwrite. Inspect the existing overwrite and include any allow and deny bits you intend to keep. Use `--target-type member` for a member ID.

Use server as code when making coordinated changes to roles, categories, channels, or role overwrites.

```sh
vox server export "$guild_id" --out current-server.yaml
vox server diff "$guild_id" desired-server.yaml --json
vox server apply "$guild_id" desired-server.yaml --dry-run --json
```

Resources are matched by name. An omitted resource is reported as extra and is retained by a normal apply. Check the diff and apply the authorized changes without `--dry-run`. Use `--prune` only when deleting the reviewed extras is part of the task. Run the diff again to verify convergence.

## Moderation and community operations

- Use `member info` to confirm a target and `server audit-log` for Discord's activity. `audit show` reads Vox's local audit trail.
- Use `member timeout`, `member kick`, `member ban`, `member unban`, or `member nick` for an authorized member action. A timeout duration is in seconds and zero clears it. Provide a reason where supported. When authorization depends on another Discord user's role, verify that role explicitly. A `--triggered-by` check can proceed if Vox cannot verify the actor.
- Use `automod list` and the relevant create, edit, enable, disable, or delete command. Inspect active rules, exemptions, and the effects of broad patterns before enabling a blocker.
- Use `event` for Discord's scheduled server events, `invite` for member invite links, `emoji` for custom emoji, and `webhook` for channel webhooks. Give an event start time with an explicit UTC offset. Keep webhook credentials and private invite links scoped to the intended audience.

Check `vox doctor --server "$guild_id" --json` for the bot's permissions and check role hierarchy or channel overwrites when Discord rejects a write. Keep the active Vox profile in effect. For a command that requires confirmation, `--yes` is the mechanism for an already authorized non-interactive operation.
