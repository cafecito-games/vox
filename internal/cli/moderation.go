package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

// MaxEmojiBytes is Discord's 256 KB ceiling for custom server emoji images.
const MaxEmojiBytes = 256 * 1024

func newEventCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "event",
		Short:   "List, create, and delete scheduled server events",
		GroupID: "moderation",
	}

	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List scheduled events in a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				evts, err := client.GetGuildScheduledEvents(guild.ID, true)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(evts))
				lines := make([]string, 0, len(evts))
				for _, e := range evts {
					var endTime, location any
					if e.ScheduledEndTime != nil {
						endTime = e.ScheduledEndTime.UTC().Format(time.RFC3339)
					}
					if e.EntityMetaData != nil && e.EntityMetaData.Location != "" {
						location = e.EntityMetaData.Location
					}
					startStr := e.ScheduledStartTime.UTC().Format(time.RFC3339)
					data = append(data, map[string]any{
						"id":          e.ID.String(),
						"name":        e.Name,
						"description": e.Description,
						"start_time":  startStr,
						"end_time":    endTime,
						"status":      fmt.Sprintf("%d", e.Status),
						"location":    location,
						"user_count":  e.UserCount,
					})
					lines = append(lines, fmt.Sprintf("%s (status:%d) - %s (ID: %s)", e.Name, e.Status, startStr, e.ID))
				}
				plain := "No events."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var evEnd, evDesc, evLocation, evChannel string
	createCmd := &cobra.Command{
		Use:   "create <server> <name> <start_time>",
		Short: "Create a scheduled server event",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			start, err := parseFlexibleISO(args[2])
			if err != nil {
				return output.UsageError(fmt.Sprintf("invalid start_time %q (use ISO 8601 like 2026-12-01T18:00:00Z)", args[2]))
			}
			create := disgodiscord.GuildScheduledEventCreate{
				Name:               args[1],
				Description:        evDesc,
				PrivacyLevel:       disgodiscord.ScheduledEventPrivacyLevelGuildOnly,
				ScheduledStartTime: start,
			}
			if evEnd != "" {
				end, err := parseFlexibleISO(evEnd)
				if err != nil {
					return output.UsageError(fmt.Sprintf("invalid --end-time %q", evEnd))
				}
				create.ScheduledEndTime = &end
			}
			if evLocation != "" {
				if evEnd == "" {
					return output.UsageError("external events require --end-time")
				}
				create.EntityType = disgodiscord.ScheduledEventEntityTypeExternal
				create.EntityMetaData = &disgodiscord.EntityMetaData{Location: evLocation}
			} else if evChannel == "" {
				return output.UsageError("specify --location (external) or --channel (voice/stage)")
			}

			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if evLocation == "" && evChannel != "" {
					ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, evChannel)
					if err != nil {
						return err
					}
					create.ChannelID = ch.ID()
					if ch.Type() == disgodiscord.ChannelTypeGuildStageVoice {
						create.EntityType = disgodiscord.ScheduledEventEntityTypeStageInstance
					} else {
						create.EntityType = disgodiscord.ScheduledEventEntityTypeVoice
					}
				}
				event, err := client.CreateGuildScheduledEvent(guild.ID, create)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": event.ID.String(), "name": event.Name},
					fmt.Sprintf("Created event '%s' (ID: %s)", event.Name, event.ID),
				)
			})
		},
	}
	createCmd.Flags().StringVar(&evEnd, "end-time", "", "End time (ISO 8601)")
	createCmd.Flags().StringVar(&evDesc, "description", "", "Event description")
	createCmd.Flags().StringVar(&evLocation, "location", "", "External event location")
	createCmd.Flags().StringVar(&evChannel, "channel", "", "Voice or stage channel name or ID")

	deleteCmd := &cobra.Command{
		Use:   "delete <server> <event_id>",
		Short: "Delete a scheduled event",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid event ID: %s", args[1]))
			}
			if err := app.ConfirmDestructive("event delete", "event "+args[1]); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				ev, err := client.GetGuildScheduledEvent(guild.ID, eventID, false)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				if err := client.DeleteGuildScheduledEvent(guild.ID, eventID); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("event delete", map[string]any{"server": args[0], "event_id": args[1]}, "ok", "")
				return app.Out.Print(map[string]any{"id": args[1], "deleted": true}, fmt.Sprintf("Deleted event '%s'", ev.Name))
			})
		},
	}

	group.AddCommand(listCmd, createCmd, deleteCmd)
	return group
}

func newInviteCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "invite",
		Short:   "Create, list, inspect, and revoke server invites",
		GroupID: "moderation",
	}

	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List active invites for a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				invites, err := client.GetGuildInvites(guild.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(invites))
				lines := make([]string, 0, len(invites))
				for _, inv := range invites {
					row := serializeExtendedInvite(inv)
					data = append(data, row)
					lines = append(lines, formatInvitePlain(row))
				}
				plain := "No active invites."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var invMaxAge, invMaxUses int
	var invTemp, invUnique, invNoUnique bool
	var invActivity, invReason string
	createCmd := &cobra.Command{
		Use:   "create <channel>",
		Short: "Create a channel invite (optionally launching a voice Activity)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if invUnique && invNoUnique {
				return output.UsageError("cannot pass both --unique and --no-unique")
			}
			var activityID snowflake.ID
			if invActivity != "" {
				var ok bool
				activityID, ok = voxdiscord.ParseSnowflake(invActivity)
				if !ok {
					return output.UsageError(fmt.Sprintf("--activity requires a numeric application ID, got: %s", invActivity))
				}
			}
			unique := !invNoUnique
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				create := disgodiscord.InviteCreate{
					MaxAge:    &invMaxAge,
					MaxUses:   &invMaxUses,
					Temporary: invTemp,
					Unique:    unique,
				}
				if activityID != 0 {
					if ch.Type() != disgodiscord.ChannelTypeGuildVoice {
						return output.UsageError(fmt.Sprintf("Activity invites require a voice channel; '%s' is not one", ch.Name()))
					}
					create.TargetType = disgodiscord.InviteTargetTypeEmbeddedApplication
					create.TargetApplicationID = activityID
				}
				var opts []rest.RequestOpt
				if invReason != "" {
					opts = append(opts, rest.WithReason(invReason))
				}
				inv, err := client.CreateInvite(ch.ID(), create, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeInvite(*inv)
				_ = app.Security.Audit.Record("invite create", map[string]any{"channel": args[0], "code": inv.Code}, "ok", "")
				return app.Out.Print(row, formatInvitePlain(row))
			})
		},
	}
	createCmd.Flags().IntVar(&invMaxAge, "max-age", 0, "Seconds until invite expires (0 = never)")
	createCmd.Flags().IntVar(&invMaxUses, "max-uses", 0, "Maximum uses (0 = unlimited)")
	createCmd.Flags().BoolVar(&invTemp, "temporary", false, "Grant temporary membership")
	createCmd.Flags().BoolVar(&invUnique, "unique", true, "Force a new invite instead of reusing")
	createCmd.Flags().BoolVar(&invNoUnique, "no-unique", false, "Allow reusing an equivalent invite")
	createCmd.Flags().StringVar(&invActivity, "activity", "", "Application ID of a Discord Activity (voice channels only)")
	createCmd.Flags().StringVar(&invReason, "reason", "", "Audit log reason")

	infoCmd := &cobra.Command{
		Use:   "info <code>",
		Short: "Inspect an invite code or URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := extractInviteCode(args[0])
			return app.WithRest(cmd, func(client rest.Rest) error {
				inv, err := client.GetInvite(code,
					rest.WithQueryParam("with_counts", true),
					rest.WithQueryParam("with_expiration", true),
				)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeInvite(*inv)
				lines := make([]string, 0, len(row))
				for k, v := range row {
					if v != nil {
						lines = append(lines, fmt.Sprintf("%s: %v", k, v))
					}
				}
				return app.Out.Print(row, strings.Join(lines, "\n"))
			})
		},
	}

	var delReason string
	deleteCmd := &cobra.Command{
		Use:   "delete <code>",
		Short: "Revoke an invite by code or URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := extractInviteCode(args[0])
			if err := app.ConfirmDestructive("invite delete", "invite "+code); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				var opts []rest.RequestOpt
				if delReason != "" {
					opts = append(opts, rest.WithReason(delReason))
				}
				if _, err := client.DeleteInvite(code, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("invite delete", map[string]any{"code": code}, "ok", "")
				return app.Out.Print(map[string]any{"code": code, "deleted": true}, fmt.Sprintf("Revoked invite %s", code))
			})
		},
	}
	deleteCmd.Flags().StringVar(&delReason, "reason", "", "Audit log reason")

	group.AddCommand(listCmd, createCmd, infoCmd, deleteCmd)
	return group
}

func newEmojiCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "emoji",
		Short:   "List, upload, rename, and delete custom server emoji",
		GroupID: "moderation",
	}

	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List custom emoji in a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				emojis, err := client.GetEmojis(guild.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(emojis))
				lines := make([]string, 0, len(emojis))
				for _, e := range emojis {
					row := serializeEmoji(e)
					data = append(data, row)
					suffix := ""
					if e.Animated {
						suffix = ", animated"
					}
					lines = append(lines, fmt.Sprintf("%s %s (ID: %s%s)", row["mention"], e.Name, e.ID, suffix))
				}
				plain := "No custom emoji."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var upRoles []string
	var upReason string
	uploadCmd := &cobra.Command{
		Use:   "upload <server> <name> <image_path>",
		Short: "Upload a custom emoji (<= 256 KB; requires Create Expressions)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := os.Stat(args[2])
			if err != nil {
				return fmt.Errorf("read emoji image %s: %w", args[2], err)
			}
			if info.Size() > MaxEmojiBytes {
				return output.UsageError(fmt.Sprintf("emoji images must be 256 KB or smaller; %s is %d KB", args[2], info.Size()/1024))
			}
			icon, err := loadDiscordIcon(args[2])
			if err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				var roleIDs []snowflake.ID
				for _, r := range upRoles {
					role, err := voxdiscord.ResolveRole(client, guild.ID, r)
					if err != nil {
						return err
					}
					roleIDs = append(roleIDs, role.ID)
				}
				var opts []rest.RequestOpt
				if upReason != "" {
					opts = append(opts, rest.WithReason(upReason))
				}
				emoji, err := client.CreateEmoji(guild.ID, disgodiscord.EmojiCreate{
					Name:  args[1],
					Image: *icon,
					Roles: roleIDs,
				}, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeEmoji(*emoji)
				_ = app.Security.Audit.Record("emoji upload", map[string]any{"server": args[0], "name": args[1], "id": emoji.ID.String()}, "ok", "")
				return app.Out.Print(row, fmt.Sprintf("Uploaded emoji %s (ID: %s)", row["mention"], emoji.ID))
			})
		},
	}
	uploadCmd.Flags().StringArrayVar(&upRoles, "role", nil, "Restrict emoji use to this role (repeatable)")
	uploadCmd.Flags().StringVar(&upReason, "reason", "", "Audit log reason")

	var renameReason string
	renameCmd := &cobra.Command{
		Use:   "rename <server> <emoji> <new_name>",
		Short: "Rename a custom emoji",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				target, err := voxdiscord.ResolveEmoji(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				previous := target.Name
				newName := args[2]
				var opts []rest.RequestOpt
				if renameReason != "" {
					opts = append(opts, rest.WithReason(renameReason))
				}
				updated, err := client.UpdateEmoji(guild.ID, target.ID, disgodiscord.EmojiUpdate{Name: &newName}, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeEmoji(*updated)
				row["previous_name"] = previous
				_ = app.Security.Audit.Record("emoji rename", map[string]any{"server": args[0], "from": previous, "to": newName}, "ok", "")
				return app.Out.Print(row, fmt.Sprintf("Renamed emoji '%s' to '%s'", previous, newName))
			})
		},
	}
	renameCmd.Flags().StringVar(&renameReason, "reason", "", "Audit log reason")

	var delReason string
	deleteCmd := &cobra.Command{
		Use:   "delete <server> <emoji>",
		Short: "Delete a custom emoji",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("emoji delete", fmt.Sprintf("emoji %s in %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				target, err := voxdiscord.ResolveEmoji(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				var opts []rest.RequestOpt
				if delReason != "" {
					opts = append(opts, rest.WithReason(delReason))
				}
				if err := client.DeleteEmoji(guild.ID, target.ID, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("emoji delete", map[string]any{"server": args[0], "name": target.Name, "id": target.ID.String()}, "ok", "")
				return app.Out.Print(
					map[string]any{"id": target.ID.String(), "name": target.Name, "deleted": true},
					fmt.Sprintf("Deleted emoji '%s'", target.Name),
				)
			})
		},
	}
	deleteCmd.Flags().StringVar(&delReason, "reason", "", "Audit log reason")

	group.AddCommand(listCmd, uploadCmd, renameCmd, deleteCmd)
	return group
}

func newAutoModCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "automod",
		Short:   "Manage Discord AutoMod rules",
		GroupID: "moderation",
	}

	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List AutoMod rules in a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				rules, err := client.GetAutoModerationRules(guild.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(rules))
				blocks := make([]string, 0, len(rules))
				for _, r := range rules {
					row := serializeAutoModRule(r)
					data = append(data, row)
					blocks = append(blocks, formatAutoModPlain(row))
				}
				plain := "No AutoMod rules."
				if len(blocks) > 0 {
					plain = strings.Join(blocks, "\n\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var (
		amTrigger, amAlertChannel, amCustomMsg, amReason string
		amKeywords, amRegexes, amPresets, amAllow        []string
		amActions, amExemptRoles, amExemptChannels       []string
		amMentionLimit, amTimeout                        int
		amEnable, amDisable                              bool
	)
	createCmd := &cobra.Command{
		Use:   "create <server> <name>",
		Short: "Create an AutoMod rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if amEnable && amDisable {
				return output.UsageError("cannot pass both --enable and --no-enable")
			}
			if cmd.Flags().Changed("timeout") && (amTimeout <= 0 || amTimeout > 2419200) {
				return output.UsageError("--timeout must be between 1 and 2419200 seconds (28 days)")
			}
			triggerType, triggerMeta, eventType, err := buildAutoModTrigger(
				amTrigger, amKeywords, amRegexes, amPresets, amAllow, amMentionLimit, cmd.Flags().Changed("mention-limit"),
			)
			if err != nil {
				return err
			}
			if len(amActions) == 0 {
				return output.UsageError("specify at least one --action (block, alert, timeout)")
			}

			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				actions, err := buildAutoModActions(client, guild.ID, amActions, amAlertChannel, amTimeout, cmd.Flags().Changed("timeout"), amCustomMsg)
				if err != nil {
					return err
				}
				var exemptRoles, exemptChannels []snowflake.ID
				for _, r := range amExemptRoles {
					role, err := voxdiscord.ResolveRole(client, guild.ID, r)
					if err != nil {
						return err
					}
					exemptRoles = append(exemptRoles, role.ID)
				}
				for _, c := range amExemptChannels {
					ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, c)
					if err != nil {
						return err
					}
					exemptChannels = append(exemptChannels, ch.ID())
				}

				enabled := !amDisable
				var opts []rest.RequestOpt
				if amReason != "" {
					opts = append(opts, rest.WithReason(amReason))
				}
				rule, err := client.CreateAutoModerationRule(guild.ID, disgodiscord.AutoModerationRuleCreate{
					Name:            args[1],
					EventType:       eventType,
					TriggerType:     triggerType,
					TriggerMetadata: triggerMeta,
					Actions:         actions,
					Enabled:         &enabled,
					ExemptRoles:     exemptRoles,
					ExemptChannels:  exemptChannels,
				}, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeAutoModRule(*rule)
				_ = app.Security.Audit.Record("automod create", map[string]any{"server": args[0], "name": args[1], "id": rule.ID.String()}, "ok", "")
				return app.Out.Print(row, "Created AutoMod rule:\n"+formatAutoModPlain(row))
			})
		},
	}
	createCmd.Flags().StringVar(&amTrigger, "trigger", "", "Trigger: keyword, spam, keyword-preset, mention-spam, member-profile (required)")
	_ = createCmd.MarkFlagRequired("trigger")
	createCmd.Flags().StringArrayVar(&amKeywords, "keyword", nil, "Keyword to match (repeatable)")
	createCmd.Flags().StringArrayVar(&amRegexes, "regex", nil, "Regex pattern to match (repeatable)")
	createCmd.Flags().StringArrayVar(&amPresets, "preset", nil, "Built-in preset: profanity, sexual-content, slurs (repeatable)")
	createCmd.Flags().StringArrayVar(&amAllow, "allow", nil, "Exempt term (repeatable)")
	createCmd.Flags().IntVar(&amMentionLimit, "mention-limit", 0, "Max unique mentions per message (for mention-spam)")
	createCmd.Flags().StringArrayVar(&amActions, "action", nil, "Action: block, alert, timeout (repeatable, required)")
	_ = createCmd.MarkFlagRequired("action")
	createCmd.Flags().StringVar(&amAlertChannel, "alert-channel", "", "Channel for --action alert")
	createCmd.Flags().IntVar(&amTimeout, "timeout", 0, "Timeout seconds for --action timeout")
	createCmd.Flags().StringVar(&amCustomMsg, "custom-message", "", "Custom block message for --action block")
	createCmd.Flags().StringArrayVar(&amExemptRoles, "exempt-role", nil, "Exempt role (repeatable)")
	createCmd.Flags().StringArrayVar(&amExemptChannels, "exempt-channel", nil, "Exempt channel (repeatable)")
	createCmd.Flags().BoolVar(&amEnable, "enable", true, "Enable rule on creation")
	createCmd.Flags().BoolVar(&amDisable, "no-enable", false, "Create rule disabled")
	createCmd.Flags().StringVar(&amReason, "reason", "", "Audit log reason")

	// automod edit
	var edName, edReason string
	var edKeywords, edRegexes, edAllow []string
	var edMentionLimit int
	editCmd := &cobra.Command{
		Use:   "edit <server> <rule>",
		Short: "Edit an existing AutoMod rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			hasTriggerEdit := len(edKeywords) > 0 || len(edRegexes) > 0 || len(edAllow) > 0 || cmd.Flags().Changed("mention-limit")
			if edName == "" && !hasTriggerEdit {
				return output.UsageError("nothing to change; pass at least one option")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				target, err := voxdiscord.ResolveAutoModRule(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				update := disgodiscord.AutoModerationRuleUpdate{}
				if edName != "" {
					update.Name = &edName
				}
				if hasTriggerEdit {
					meta := target.TriggerMetadata
					if len(edKeywords) > 0 {
						meta.KeywordFilter = edKeywords
					}
					if len(edRegexes) > 0 {
						meta.RegexPatterns = edRegexes
					}
					if len(edAllow) > 0 {
						meta.AllowList = edAllow
					}
					if cmd.Flags().Changed("mention-limit") {
						meta.MentionTotalLimit = edMentionLimit
					}
					update.TriggerMetadata = &meta
				}
				var opts []rest.RequestOpt
				if edReason != "" {
					opts = append(opts, rest.WithReason(edReason))
				}
				updated, err := client.UpdateAutoModerationRule(guild.ID, target.ID, update, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				row := serializeAutoModRule(*updated)
				_ = app.Security.Audit.Record("automod edit", map[string]any{"server": args[0], "rule": args[1]}, "ok", "")
				return app.Out.Print(row, "Updated AutoMod rule:\n"+formatAutoModPlain(row))
			})
		},
	}
	editCmd.Flags().StringVar(&edName, "name", "", "New rule name")
	editCmd.Flags().StringArrayVar(&edKeywords, "keyword", nil, "Replace keyword list (repeatable)")
	editCmd.Flags().StringArrayVar(&edRegexes, "regex", nil, "Replace regex list (repeatable)")
	editCmd.Flags().StringArrayVar(&edAllow, "allow", nil, "Replace allow list (repeatable)")
	editCmd.Flags().IntVar(&edMentionLimit, "mention-limit", 0, "New mention limit")
	editCmd.Flags().StringVar(&edReason, "reason", "", "Audit log reason")

	var enReason, disReason, delReason string
	enableCmd := &cobra.Command{
		Use:   "enable <server> <rule>",
		Short: "Enable an AutoMod rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setAutoModEnabled(app, cmd, args[0], args[1], true, enReason)
		},
	}
	enableCmd.Flags().StringVar(&enReason, "reason", "", "Audit log reason")

	disableCmd := &cobra.Command{
		Use:   "disable <server> <rule>",
		Short: "Disable an AutoMod rule without deleting it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setAutoModEnabled(app, cmd, args[0], args[1], false, disReason)
		},
	}
	disableCmd.Flags().StringVar(&disReason, "reason", "", "Audit log reason")

	deleteCmd := &cobra.Command{
		Use:   "delete <server> <rule>",
		Short: "Delete an AutoMod rule",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("automod delete", fmt.Sprintf("rule %s in %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				target, err := voxdiscord.ResolveAutoModRule(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				var opts []rest.RequestOpt
				if delReason != "" {
					opts = append(opts, rest.WithReason(delReason))
				}
				if err := client.DeleteAutoModerationRule(guild.ID, target.ID, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("automod delete", map[string]any{"server": args[0], "name": target.Name, "id": target.ID.String()}, "ok", "")
				return app.Out.Print(
					map[string]any{"id": target.ID.String(), "name": target.Name, "deleted": true},
					fmt.Sprintf("Deleted AutoMod rule '%s'", target.Name),
				)
			})
		},
	}
	deleteCmd.Flags().StringVar(&delReason, "reason", "", "Audit log reason")

	group.AddCommand(listCmd, createCmd, editCmd, enableCmd, disableCmd, deleteCmd)
	return group
}

func setAutoModEnabled(app *App, cmd *cobra.Command, serverRef, ruleRef string, enabled bool, reason string) error {
	return app.WithRest(cmd, func(client rest.Rest) error {
		guild, err := voxdiscord.ResolveGuild(client, serverRef)
		if err != nil {
			return err
		}
		target, err := voxdiscord.ResolveAutoModRule(client, guild.ID, ruleRef)
		if err != nil {
			return err
		}
		var opts []rest.RequestOpt
		if reason != "" {
			opts = append(opts, rest.WithReason(reason))
		}
		updated, err := client.UpdateAutoModerationRule(guild.ID, target.ID, disgodiscord.AutoModerationRuleUpdate{
			Enabled: &enabled,
		}, opts...)
		if err != nil {
			return voxdiscord.MapError(err)
		}
		row := serializeAutoModRule(*updated)
		row["enabled"] = enabled
		verb := "Disabled"
		auditName := "automod disable"
		if enabled {
			verb = "Enabled"
			auditName = "automod enable"
		}
		_ = app.Security.Audit.Record(auditName, map[string]any{"server": serverRef, "rule": ruleRef}, "ok", "")
		return app.Out.Print(row, fmt.Sprintf("%s AutoMod rule '%s'", verb, updated.Name))
	})
}

func buildAutoModTrigger(
	trigger string,
	keywords, regexes, presets, allow []string,
	mentionLimit int,
	hasMentionLimit bool,
) (disgodiscord.AutoModerationTriggerType, *disgodiscord.AutoModerationTriggerMetadata, disgodiscord.AutoModerationEventType, error) {
	eventType := disgodiscord.AutoModerationEventTypeMessageSend
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "keyword":
		if len(keywords) == 0 && len(regexes) == 0 {
			return 0, nil, 0, output.UsageError("--trigger keyword requires at least one --keyword or --regex")
		}
		return disgodiscord.AutoModerationTriggerTypeKeyword, &disgodiscord.AutoModerationTriggerMetadata{
			KeywordFilter: keywords,
			RegexPatterns: regexes,
			AllowList:     allow,
		}, eventType, nil
	case "keyword-preset":
		if len(presets) == 0 {
			return 0, nil, 0, output.UsageError("--trigger keyword-preset requires at least one --preset (profanity, sexual-content, slurs)")
		}
		presetList := make([]disgodiscord.AutoModerationKeywordPreset, 0, len(presets))
		for _, p := range presets {
			switch strings.ToLower(p) {
			case "profanity":
				presetList = append(presetList, disgodiscord.AutoModerationKeywordPresetProfanity)
			case "sexual-content", "sexual_content":
				presetList = append(presetList, disgodiscord.AutoModerationKeywordPresetSexualContent)
			case "slurs":
				presetList = append(presetList, disgodiscord.AutoModerationKeywordPresetSlurs)
			default:
				return 0, nil, 0, output.UsageError(fmt.Sprintf("unknown --preset %q", p))
			}
		}
		return disgodiscord.AutoModerationTriggerTypeKeywordPresent, &disgodiscord.AutoModerationTriggerMetadata{
			Presets:   presetList,
			AllowList: allow,
		}, eventType, nil
	case "mention-spam":
		if !hasMentionLimit {
			return 0, nil, 0, output.UsageError("--trigger mention-spam requires --mention-limit")
		}
		return disgodiscord.AutoModerationTriggerTypeMentionSpam, &disgodiscord.AutoModerationTriggerMetadata{
			MentionTotalLimit: mentionLimit,
		}, eventType, nil
	case "spam":
		return disgodiscord.AutoModerationTriggerTypeSpam, &disgodiscord.AutoModerationTriggerMetadata{}, eventType, nil
	case "member-profile":
		if len(keywords) == 0 && len(regexes) == 0 {
			return 0, nil, 0, output.UsageError("--trigger member-profile requires at least one --keyword or --regex")
		}
		return disgodiscord.AutoModerationTriggerTypeMemberProfile, &disgodiscord.AutoModerationTriggerMetadata{
			KeywordFilter: keywords,
			RegexPatterns: regexes,
			AllowList:     allow,
		}, disgodiscord.AutoModerationEventTypeMemberUpdate, nil
	default:
		return 0, nil, 0, output.UsageError(fmt.Sprintf("unknown --trigger %q (use keyword, spam, keyword-preset, mention-spam, member-profile)", trigger))
	}
}

func buildAutoModActions(
	client rest.Rest,
	guildID snowflake.ID,
	actionNames []string,
	alertChannel string,
	timeout int,
	hasTimeout bool,
	customMsg string,
) ([]disgodiscord.AutoModerationAction, error) {
	built := make([]disgodiscord.AutoModerationAction, 0, len(actionNames))
	for _, name := range actionNames {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "block":
			action := disgodiscord.AutoModerationAction{Type: disgodiscord.AutoModerationActionTypeBlockMessage}
			if customMsg != "" {
				msgCopy := customMsg
				action.Metadata = &disgodiscord.AutoModerationActionMetadata{CustomMessage: &msgCopy}
			}
			built = append(built, action)
		case "alert":
			if alertChannel == "" {
				return nil, output.UsageError("--action alert requires --alert-channel")
			}
			ch, err := voxdiscord.ResolveChannelInGuild(client, guildID, alertChannel)
			if err != nil {
				return nil, err
			}
			built = append(built, disgodiscord.AutoModerationAction{
				Type:     disgodiscord.AutoModerationActionTypeSendAlertMessage,
				Metadata: &disgodiscord.AutoModerationActionMetadata{ChannelID: ch.ID()},
			})
		case "timeout":
			if !hasTimeout {
				return nil, output.UsageError("--action timeout requires --timeout")
			}
			built = append(built, disgodiscord.AutoModerationAction{
				Type:     disgodiscord.AutoModerationActionTypeTimeout,
				Metadata: &disgodiscord.AutoModerationActionMetadata{DurationSeconds: timeout},
			})
		default:
			return nil, output.UsageError(fmt.Sprintf("unknown --action %q (use block, alert, timeout)", name))
		}
	}
	return built, nil
}

func serializeAutoModRule(r disgodiscord.AutoModerationRule) map[string]any {
	presets := make([]string, 0, len(r.TriggerMetadata.Presets))
	for _, p := range r.TriggerMetadata.Presets {
		switch p {
		case disgodiscord.AutoModerationKeywordPresetProfanity:
			presets = append(presets, "profanity")
		case disgodiscord.AutoModerationKeywordPresetSexualContent:
			presets = append(presets, "sexual_content")
		case disgodiscord.AutoModerationKeywordPresetSlurs:
			presets = append(presets, "slurs")
		}
	}
	actions := make([]map[string]any, 0, len(r.Actions))
	for _, a := range r.Actions {
		entry := map[string]any{"type": fmt.Sprintf("%d", a.Type)}
		if a.Metadata != nil {
			if a.Metadata.ChannelID != 0 {
				entry["channel_id"] = a.Metadata.ChannelID.String()
			}
			if a.Metadata.DurationSeconds > 0 {
				entry["duration_seconds"] = a.Metadata.DurationSeconds
			}
			if a.Metadata.CustomMessage != nil && *a.Metadata.CustomMessage != "" {
				entry["custom_message"] = *a.Metadata.CustomMessage
			}
		}
		actions = append(actions, entry)
	}
	exemptRoles := make([]string, len(r.ExemptRoles))
	for i, id := range r.ExemptRoles {
		exemptRoles[i] = id.String()
	}
	exemptChs := make([]string, len(r.ExemptChannels))
	for i, id := range r.ExemptChannels {
		exemptChs[i] = id.String()
	}
	return map[string]any{
		"id":                 r.ID.String(),
		"name":               r.Name,
		"enabled":            r.Enabled,
		"trigger":            fmt.Sprintf("%d", r.TriggerType),
		"keywords":           r.TriggerMetadata.KeywordFilter,
		"regex_patterns":     r.TriggerMetadata.RegexPatterns,
		"allow_list":         r.TriggerMetadata.AllowList,
		"mention_limit":      r.TriggerMetadata.MentionTotalLimit,
		"presets":            presets,
		"actions":            actions,
		"exempt_role_ids":    exemptRoles,
		"exempt_channel_ids": exemptChs,
		"creator_id":         r.CreatorID.String(),
	}
}

func formatAutoModPlain(data map[string]any) string {
	state := "disabled"
	if enabled, _ := data["enabled"].(bool); enabled {
		state = "enabled"
	}
	lines := []string{
		fmt.Sprintf("%v [%s] (ID: %v)", data["name"], state, data["id"]),
		fmt.Sprintf("  trigger: %v", data["trigger"]),
	}
	if kws, ok := data["keywords"].([]string); ok && len(kws) > 0 {
		lines = append(lines, "  keywords: "+strings.Join(kws, ", "))
	}
	if rx, ok := data["regex_patterns"].([]string); ok && len(rx) > 0 {
		lines = append(lines, "  regex: "+strings.Join(rx, ", "))
	}
	if ps, ok := data["presets"].([]string); ok && len(ps) > 0 {
		lines = append(lines, "  presets: "+strings.Join(ps, ", "))
	}
	return strings.Join(lines, "\n")
}

func serializeEmoji(e disgodiscord.Emoji) map[string]any {
	mention := fmt.Sprintf("<:%s:%s>", e.Name, e.ID)
	if e.Animated {
		mention = fmt.Sprintf("<a:%s:%s>", e.Name, e.ID)
	}
	return map[string]any{
		"id":        e.ID.String(),
		"name":      e.Name,
		"mention":   mention,
		"animated":  e.Animated,
		"available": e.Available,
		"managed":   e.Managed,
		"url":       e.URL(),
	}
}

func serializeInvite(inv disgodiscord.Invite) map[string]any {
	row := map[string]any{
		"code":           inv.Code,
		"url":            inv.URL(),
		"member_count":   inv.ApproximateMemberCount,
		"presence_count": inv.ApproximatePresenceCount,
	}
	if inv.Channel != nil {
		row["channel"] = inv.Channel.Name
		row["channel_id"] = inv.Channel.ID.String()
	}
	if inv.Guild != nil {
		row["guild"] = inv.Guild.Name
	}
	if inv.Inviter != nil {
		row["inviter"] = voxdiscord.FormatUserTag(*inv.Inviter)
	}
	if inv.ExpiresAt != nil {
		row["expires_at"] = inv.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return row
}

func serializeExtendedInvite(inv disgodiscord.ExtendedInvite) map[string]any {
	row := serializeInvite(inv.Invite)
	row["uses"] = inv.Uses
	row["max_uses"] = inv.MaxUses
	row["max_age"] = inv.MaxAge
	row["temporary"] = inv.Temporary
	row["created_at"] = inv.CreatedAt.UTC().Format(time.RFC3339)
	return row
}

func formatInvitePlain(row map[string]any) string {
	parts := []string{fmt.Sprint(row["url"])}
	if ch, ok := row["channel"].(string); ok && ch != "" {
		parts = append(parts, "-> #"+ch)
	}
	if uses, ok := row["uses"].(int); ok {
		maxUses := "unlimited"
		if m, ok := row["max_uses"].(int); ok && m > 0 {
			maxUses = fmt.Sprintf("%d", m)
		}
		parts = append(parts, fmt.Sprintf("uses: %d/%s", uses, maxUses))
	}
	if inviter, ok := row["inviter"].(string); ok && inviter != "" {
		parts = append(parts, "by "+inviter)
	}
	return strings.Join(parts, " | ")
}

func extractInviteCode(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx+1 < len(trimmed) {
		return trimmed[idx+1:]
	}
	return trimmed
}

func parseFlexibleISO(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid ISO 8601 time %q", raw)
}
