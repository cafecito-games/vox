# Bot provisioning and application settings

Use `vox bot provision <spec> --json` to set up one bot for a target server and resume after Discord's human handoffs.

```yaml
version: 1
name: release-bot
guild_id: "123456789012345678"
permissions: [view_channel, send_messages]
profile:
  username: ReleaseBot
application:
  description: Publishes release updates
  tags: [releases, automation]
```

Use the same manifest name and Vox config directory on each run. Supply the application ID in `application_id` or pass `--application-id` for the first verified binding. The runner injects the matching token as `DISCORD_BOT_TOKEN`. Store the token in the runner's secret store. The user should enter it there directly.

| Status | Action |
| --- | --- |
| `needs_discord_app` | Give the app owner `portal_url` to create or select the application and provide its ID. |
| `needs_bot_token` | Give the app owner `portal_url` to issue or recover the credential through Discord's token-reset flow. |
| `needs_install_approval` | Give the authorized server member `install_url` or direct them to the app's configured OAuth2 install flow if Vox returns a Portal link. |
| `needs_permissions` | Report `missing_permissions` and have the applicable server role or permissions updated. |
| `ready` | Vox verified server membership and the requested server-level permissions. |

Pending statuses exit successfully. Branch on the JSON status and resume when the prerequisite is complete. Reuse the registered application for an existing bot. Treat an application ID mismatch as a credential or target selection error before trying again.

## Change an existing bot

- Use `vox bot show --json` before edits to check the bot identity and current settings. Use `bot profile edit` for username, avatar, or banner and `bot application edit` for description, tags, app images, install settings, and limited intent flags.
- `bot invite --guild-id` generates an app installation link. `--application-id` allows offline generation. Select explicit `--permissions` or suitable `--bundle` values. Discord asks the authorized server member to approve.
- Top-level provision `permissions` are the invite request and the minimum live server permissions. The optional `application.guild_install_permissions` sets app-wide defaults for future installs. For bots already installed, change live access through server roles or channel overwrites and verify with `doctor --server`.
- App creation and token reset stay in the Developer Portal. Use the limited Gateway intent flags only for features that need them. Discord handles privileged-intent reviews.
