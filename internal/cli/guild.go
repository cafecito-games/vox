package cli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/serverspec"
)

func newServerCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "server",
		Short:   "Inspect, edit, audit, onboard, and manage servers as code",
		GroupID: "server",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List servers the bot belongs to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guilds, err := voxdiscord.FetchAllGuilds(client)
				if err != nil {
					return err
				}
				data := make([]map[string]any, 0, len(guilds))
				lines := make([]string, 0, len(guilds))
				for _, g := range guilds {
					data = append(data, map[string]any{
						"id":           g.ID.String(),
						"name":         g.Name,
						"member_count": g.ApproximateMemberCount,
					})
					lines = append(lines, fmt.Sprintf("%s (ID: %s, members: %d)", g.Name, g.ID, g.ApproximateMemberCount))
				}
				plain := "Bot is not in any servers."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	infoCmd := &cobra.Command{
		Use:   "info <server>",
		Short: "Show server details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				channels, _ := client.GetGuildChannels(guild.ID)
				roles, _ := client.GetRoles(guild.ID)
				owner := guild.OwnerID.String()
				if ownerMember, err := client.GetMember(guild.ID, guild.OwnerID); err == nil {
					owner = voxdiscord.FormatUserTag(ownerMember.User)
				}
				createdAt := guild.ID.Time().UTC().Format(time.RFC3339)
				data := map[string]any{
					"id":            guild.ID.String(),
					"name":          guild.Name,
					"owner":         owner,
					"member_count":  guild.ApproximateMemberCount,
					"channel_count": len(channels),
					"role_count":    len(roles),
					"created_at":    createdAt,
				}
				plain := strings.Join([]string{
					fmt.Sprintf("id: %s", guild.ID),
					fmt.Sprintf("name: %s", guild.Name),
					fmt.Sprintf("owner: %s", owner),
					fmt.Sprintf("member_count: %d", guild.ApproximateMemberCount),
					fmt.Sprintf("channel_count: %d", len(channels)),
					fmt.Sprintf("role_count: %d", len(roles)),
					fmt.Sprintf("created_at: %s", createdAt),
				}, "\n")
				err = app.Out.Print(data, plain)
				app.Out.WarnChannelVisibility()
				return err
			})
		},
	}

	// server audit-log
	var auditLimit int
	var auditAction, auditUser string
	var auditShowChanges bool
	auditLogCmd := &cobra.Command{
		Use:   "audit-log <server>",
		Short: "Read Discord's server audit log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				var userID snowflake.ID
				if auditUser != "" {
					if id, ok := voxdiscord.ParseSnowflake(auditUser); ok {
						userID = id
					} else {
						m, err := voxdiscord.ResolveMember(client, guild.ID, auditUser)
						if err != nil {
							return err
						}
						userID = m.User.ID
					}
				}
				var actionType disgodiscord.AuditLogEvent
				if auditAction != "" {
					parsed, ok := parseAuditAction(auditAction)
					if !ok {
						return output.UsageError(fmt.Sprintf("unknown audit log action %q", auditAction))
					}
					actionType = parsed
				}
				logData, err := client.GetAuditLog(guild.ID, userID, actionType, 0, 0, auditLimit)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				usersByID := make(map[snowflake.ID]string, len(logData.Users))
				for _, u := range logData.Users {
					usersByID[u.ID] = voxdiscord.FormatUserTag(u)
				}
				out := make([]map[string]any, 0, len(logData.AuditLogEntries))
				lines := make([]string, 0, len(logData.AuditLogEntries))
				for _, e := range logData.AuditLogEntries {
					actor := "unknown"
					var actorID any
					if e.UserID != 0 {
						actorID = e.UserID.String()
						if name, ok := usersByID[e.UserID]; ok {
							actor = name
						} else {
							actor = e.UserID.String()
						}
					}
					var targetID any
					if e.TargetID != nil {
						targetID = e.TargetID.String()
					}
					changedKeys := make([]string, 0, len(e.Changes))
					changesMap := make(map[string]any, len(e.Changes))
					for _, ch := range e.Changes {
						changedKeys = append(changedKeys, string(ch.Key))
						changesMap[string(ch.Key)] = map[string]any{"before": ch.OldValue, "after": ch.NewValue}
					}
					item := map[string]any{
						"id":        e.ID.String(),
						"action":    fmt.Sprintf("%d", e.ActionType),
						"user":      actor,
						"user_id":   actorID,
						"target_id": targetID,
						"reason":    e.Reason,
						"timestamp": e.ID.Time().UTC().Format(time.RFC3339),
						"changed":   changedKeys,
					}
					if auditShowChanges {
						item["changes"] = changesMap
					}
					out = append(out, item)
					line := fmt.Sprintf("[%s] %s -> action:%d", e.ID.Time().UTC().Format("2006-01-02 15:04:05"), actor, e.ActionType)
					if targetID != nil {
						line += fmt.Sprintf(" on %v", targetID)
					}
					if len(changedKeys) > 0 {
						line += " (" + strings.Join(changedKeys, ", ") + ")"
					}
					if e.Reason != nil && *e.Reason != "" {
						line += " -- " + *e.Reason
					}
					lines = append(lines, line)
				}
				plain := "No audit log entries."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(out, plain)
			})
		},
	}
	auditLogCmd.Flags().IntVar(&auditLimit, "limit", 50, "Maximum entries to fetch")
	auditLogCmd.Flags().StringVar(&auditAction, "action", "", "Filter by audit action name or numeric code")
	auditLogCmd.Flags().StringVar(&auditUser, "user", "", "Filter by actor user ID or username")
	auditLogCmd.Flags().BoolVar(&auditShowChanges, "changes", false, "Include before/after field diffs in JSON output")

	// server edit
	var editName, editDesc, editIcon, editBanner, editVerify, editSysCh, editReason string
	editCmd := &cobra.Command{
		Use:   "edit <server>",
		Short: "Edit server name, description, icon, banner, verification level, or system channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				update := disgodiscord.GuildUpdate{}
				var changed []string
				if cmd.Flags().Changed("name") {
					update.Name = &editName
					changed = append(changed, "name")
				}
				if cmd.Flags().Changed("description") {
					update.Description = &editDesc
					changed = append(changed, "description")
				}
				if editIcon != "" {
					icon, err := loadDiscordIcon(editIcon)
					if err != nil {
						return err
					}
					update.Icon = omit.New(icon)
					changed = append(changed, "icon")
				}
				if editBanner != "" {
					banner, err := loadDiscordIcon(editBanner)
					if err != nil {
						return err
					}
					update.Banner = omit.New(banner)
					changed = append(changed, "banner")
				}
				if editVerify != "" {
					level, err := serverspec.ParseVerificationLevel(editVerify)
					if err != nil {
						return err
					}
					update.VerificationLevel = omit.New(&level)
					changed = append(changed, "verification_level")
				}
				if editSysCh != "" {
					ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, editSysCh)
					if err != nil {
						return err
					}
					id := ch.ID()
					update.SystemChannelID = &id
					changed = append(changed, "system_channel")
				}
				if len(changed) == 0 {
					return output.UsageError("no changes specified")
				}
				slices.Sort(changed)
				var opts []rest.RequestOpt
				if editReason != "" {
					opts = append(opts, rest.WithReason(editReason))
				}
				updated, err := client.UpdateGuild(guild.ID, update, opts...)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("server edit", map[string]any{"server": args[0], "changed": changed}, "ok", "")
				return app.Out.Print(
					map[string]any{"id": updated.ID.String(), "name": updated.Name, "changed": changed},
					fmt.Sprintf("Updated %s: %s", updated.Name, strings.Join(changed, ", ")),
				)
			})
		},
	}
	editCmd.Flags().StringVar(&editName, "name", "", "New server name")
	editCmd.Flags().StringVar(&editDesc, "description", "", "New server description")
	editCmd.Flags().StringVar(&editIcon, "icon", "", "Path to new icon image")
	editCmd.Flags().StringVar(&editBanner, "banner", "", "Path to new banner image")
	editCmd.Flags().StringVar(&editVerify, "verification-level", "", "Verification level: none, low, medium, high, highest")
	editCmd.Flags().StringVar(&editSysCh, "system-channel", "", "System messages channel name or ID")
	editCmd.Flags().StringVar(&editReason, "reason", "", "Audit log reason")

	// server onboarding subgroup
	onboardingGroup := &cobra.Command{
		Use:   "onboarding",
		Short: "Inspect and configure Discord server onboarding",
	}
	onboardingShowCmd := &cobra.Command{
		Use:   "show <server>",
		Short: "Show server onboarding configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				ob, err := client.GetGuildOnboarding(guild.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				defaultChs := make([]string, 0, len(ob.DefaultChannelIDs))
				for _, id := range ob.DefaultChannelIDs {
					defaultChs = append(defaultChs, id.String())
				}
				slices.Sort(defaultChs)
				prompts := make([]map[string]any, 0, len(ob.Prompts))
				lines := []string{
					fmt.Sprintf("enabled: %v", ob.Enabled),
					fmt.Sprintf("mode: %d", ob.Mode),
					fmt.Sprintf("default channels: %s", noneOrJoin(defaultChs)),
				}
				for _, p := range ob.Prompts {
					opts := make([]map[string]any, 0, len(p.Options))
					for _, o := range p.Options {
						rids := make([]string, len(o.RoleIDs))
						for i, id := range o.RoleIDs {
							rids[i] = id.String()
						}
						cids := make([]string, len(o.ChannelIDs))
						for i, id := range o.ChannelIDs {
							cids[i] = id.String()
						}
						opts = append(opts, map[string]any{
							"id":          o.ID.String(),
							"title":       o.Title,
							"description": o.Description,
							"role_ids":    rids,
							"channel_ids": cids,
						})
					}
					prompts = append(prompts, map[string]any{
						"id":            p.ID.String(),
						"title":         p.Title,
						"type":          p.Type,
						"single_select": p.SingleSelect,
						"required":      p.Required,
						"in_onboarding": p.InOnboarding,
						"options":       opts,
					})
					lines = append(lines, fmt.Sprintf("prompt: %s (ID: %s)", p.Title, p.ID))
					for _, o := range p.Options {
						lines = append(lines, "    - "+o.Title)
					}
				}
				return app.Out.Print(map[string]any{
					"enabled":             ob.Enabled,
					"mode":                ob.Mode,
					"default_channel_ids": defaultChs,
					"prompts":             prompts,
				}, strings.Join(lines, "\n"))
			})
		},
	}

	var obEnable, obDisable bool
	var obMode, obReason string
	var obDefaultChannels []string
	onboardingEditCmd := &cobra.Command{
		Use:   "edit <server>",
		Short: "Edit server onboarding settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if obEnable && obDisable {
				return output.UsageError("cannot pass both --enable and --disable")
			}
			if !obEnable && !obDisable && obMode == "" && len(obDefaultChannels) == 0 {
				return output.UsageError("specify at least one of --enable, --disable, --mode, or --default-channel")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				update := disgodiscord.GuildOnboardingUpdate{}
				var changed []string
				if obEnable {
					t := true
					update.Enabled = &t
					changed = append(changed, "enabled")
				} else if obDisable {
					f := false
					update.Enabled = &f
					changed = append(changed, "enabled")
				}
				if obMode != "" {
					var mode disgodiscord.GuildOnboardingMode
					switch strings.ToLower(obMode) {
					case "default":
						mode = disgodiscord.GuildOnboardingModeDefault
					case "advanced":
						mode = disgodiscord.GuildOnboardingModeAdvanced
					default:
						return output.UsageError("invalid --mode (use default or advanced)")
					}
					update.Mode = &mode
					changed = append(changed, "mode")
				}
				if len(obDefaultChannels) > 0 {
					ids := make([]snowflake.ID, 0, len(obDefaultChannels))
					for _, chRef := range obDefaultChannels {
						ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, chRef)
						if err != nil {
							return err
						}
						ids = append(ids, ch.ID())
					}
					update.DefaultChannelIDs = &ids
					changed = append(changed, "default_channels")
				}
				var opts []rest.RequestOpt
				if obReason != "" {
					opts = append(opts, rest.WithReason(obReason))
				}
				if _, err := client.UpdateGuildOnboarding(guild.ID, update, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("server onboarding edit", map[string]any{"server": args[0], "changed": changed}, "ok", "")
				return app.Out.Print(
					map[string]any{"server": guild.Name, "changed": changed},
					fmt.Sprintf("Updated onboarding for %s: %s", guild.Name, strings.Join(changed, ", ")),
				)
			})
		},
	}
	onboardingEditCmd.Flags().BoolVar(&obEnable, "enable", false, "Enable onboarding")
	onboardingEditCmd.Flags().BoolVar(&obDisable, "disable", false, "Disable onboarding")
	onboardingEditCmd.Flags().StringVar(&obMode, "mode", "", "Onboarding mode: default or advanced")
	onboardingEditCmd.Flags().StringArrayVar(&obDefaultChannels, "default-channel", nil, "Default channel name or ID (repeatable, replaces full list)")
	onboardingEditCmd.Flags().StringVar(&obReason, "reason", "", "Audit log reason")
	onboardingGroup.AddCommand(onboardingShowCmd, onboardingEditCmd)

	// server export / diff / apply (Server as Code)
	var exportOut string
	exportCmd := &cobra.Command{
		Use:   "export <server>",
		Short: "Export roles, categories, channels, and overwrites to JSON or YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				spec, err := serverspec.Export(client, guild)
				if err != nil {
					return err
				}
				encoded, err := serverspec.Marshal(spec, exportOut)
				if err != nil {
					return err
				}
				if exportOut != "" {
					if err := os.WriteFile(exportOut, encoded, 0o600); err != nil {
						return err
					}
					return app.Out.Print(spec, fmt.Sprintf("Wrote %s — %d role(s), %d categor(ies), %d channel(s)",
						exportOut, len(spec.Roles), len(spec.Categories), len(spec.Channels)))
				}
				return app.Out.PrintRaw(string(encoded))
			})
		},
	}
	exportCmd.Flags().StringVar(&exportOut, "out", "", "Output file path (.yaml/.yml for YAML, otherwise JSON)")

	var diffPrune, diffShowUnchanged bool
	diffCmd := &cobra.Command{
		Use:   "diff <server> <spec_file>",
		Short: "Compare a live server against a declarative YAML/JSON spec",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			desired, err := serverspec.LoadFile(args[1])
			if err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				actual, err := serverspec.Export(client, guild)
				if err != nil {
					return err
				}
				diff, err := serverspec.Diff(actual, desired, guild.Name)
				if err != nil {
					return err
				}
				filtered := diff
				if !diffShowUnchanged {
					filtered.Changes = make([]serverspec.Change, 0, len(diff.Changes))
					for _, c := range diff.Changes {
						if c.Op != serverspec.OpUnchanged {
							filtered.Changes = append(filtered.Changes, c)
						}
					}
				}
				plain := serverspec.RenderChanges(diff.Changes, diffPrune, diffShowUnchanged)
				if len(filtered.Changes) > 0 {
					plain += fmt.Sprintf("\n\n%d to create, %d to update, %d not in spec",
						diff.Counts.Create, diff.Counts.Update, diff.Counts.Extra)
				}
				return app.Out.Print(filtered, plain)
			})
		},
	}
	diffCmd.Flags().BoolVar(&diffPrune, "prune", false, "Preview deletions for resources absent from spec")
	diffCmd.Flags().BoolVar(&diffShowUnchanged, "show-unchanged", false, "Include unchanged items in output")

	var applyDryRun, applyPrune bool
	var applyReason string
	applyCmd := &cobra.Command{
		Use:   "apply <server> <spec_file>",
		Short: "Reconcile a live server to match a declarative YAML/JSON spec (additive by default)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			desired, err := serverspec.LoadFile(args[1])
			if err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				res, err := serverspec.Apply(client, guild, desired, serverspec.ApplyOptions{
					DryRun: applyDryRun,
					Prune:  applyPrune,
					Reason: applyReason,
					ConfirmPrune: func(extras []serverspec.Change) error {
						names := make([]string, 0, min(len(extras), 8))
						for i := 0; i < len(extras) && i < 8; i++ {
							names = append(names, extras[i].Kind+" "+extras[i].Name)
						}
						listing := strings.Join(names, ", ")
						if len(extras) > 8 {
							listing += fmt.Sprintf(", and %d more", len(extras)-8)
						}
						return app.ConfirmDestructive("server apply --prune", fmt.Sprintf("delete %d: %s", len(extras), listing))
					},
				})
				if err != nil {
					return err
				}
				if applyDryRun {
					plain := serverspec.RenderChanges(res.Changes, applyPrune, false) + "\n\n(dry run — nothing was written)"
					return app.Out.Print(res, plain)
				}
				_ = app.Security.Audit.Record("server apply", map[string]any{
					"server":  args[0],
					"spec":    args[1],
					"applied": len(res.Applied),
					"pruned":  len(res.Pruned),
				}, "ok", "")
				plain := fmt.Sprintf("Applied %d change(s) to %s.", len(res.Applied), res.Server)
				if len(res.Pruned) > 0 {
					plain += fmt.Sprintf("\nDeleted %d: %s", len(res.Pruned), strings.Join(res.Pruned, ", "))
				} else if len(res.LeftAlone) > 0 {
					plain += fmt.Sprintf("\n%d item(s) not in the spec were left alone (use --prune to delete).", len(res.LeftAlone))
				}
				return app.Out.Print(res, plain)
			})
		},
	}
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "Show planned changes without writing to Discord")
	applyCmd.Flags().BoolVar(&applyPrune, "prune", false, "Delete resources not declared in the spec (requires confirmation)")
	applyCmd.Flags().StringVar(&applyReason, "reason", "", "Audit log reason")

	group.AddCommand(listCmd, infoCmd, auditLogCmd, editCmd, onboardingGroup, exportCmd, diffCmd, applyCmd)
	return group
}

func newRoleCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "role",
		Short:   "List, create, edit, delete, assign, and remove server roles",
		GroupID: "server",
	}

	var withMemberCounts bool
	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List roles in a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				roles, err := client.GetRoles(guild.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				var counts map[snowflake.ID]int
				if withMemberCounts {
					if fetched, err := client.GetRoleMemberCounts(guild.ID); err == nil {
						counts = fetched
					}
				}
				data := make([]map[string]any, 0, len(roles))
				lines := make([]string, 0, len(roles))
				for _, r := range roles {
					if r.ID == guild.ID || r.Name == "@everyone" {
						continue
					}
					var memberCount any
					memberLabel := "unavailable"
					if counts != nil {
						c := counts[r.ID]
						memberCount = c
						memberLabel = strconv.Itoa(c)
					}
					color := fmt.Sprintf("#%06x", r.Color)
					data = append(data, map[string]any{
						"id":      r.ID.String(),
						"name":    r.Name,
						"color":   color,
						"members": memberCount,
					})
					lines = append(lines, fmt.Sprintf("%s (ID: %s, color: %s, members: %s)", r.Name, r.ID, color, memberLabel))
				}
				plain := "No roles."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}
	listCmd.Flags().BoolVar(&withMemberCounts, "with-member-counts", false, "Include per-role member counts (one extra REST call)")

	var createColor, createPerms string
	createCmd := &cobra.Command{
		Use:   "create <server> <name>",
		Short: "Create a role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			create := disgodiscord.RoleCreate{Name: args[1]}
			if createColor != "" {
				c, err := voxdiscord.ParseHexColor(createColor)
				if err != nil {
					return err
				}
				create.Color = c
			}
			if createPerms != "" {
				bits, err := strconv.ParseInt(createPerms, 10, 64)
				if err != nil {
					return output.UsageError(fmt.Sprintf("invalid --permissions integer: %s", createPerms))
				}
				p := disgodiscord.Permissions(bits)
				create.Permissions = &p
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				role, err := client.CreateRole(guild.ID, create)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": role.ID.String(), "name": role.Name},
					fmt.Sprintf("Created role %s (ID: %s)", role.Name, role.ID),
				)
			})
		},
	}
	createCmd.Flags().StringVar(&createColor, "color", "", "Hex color (e.g. ff0000)")
	createCmd.Flags().StringVar(&createPerms, "permissions", "", "Permission integer bitfield")

	deleteCmd := &cobra.Command{
		Use:   "delete <server> <role>",
		Short: "Delete a role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("role delete", fmt.Sprintf("%s in %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				role, err := voxdiscord.ResolveRole(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				if err := client.DeleteRole(guild.ID, role.ID); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("role delete", map[string]any{"server": args[0], "role": role.Name}, "ok", "")
				return app.Out.Print(map[string]any{"name": role.Name, "deleted": true}, fmt.Sprintf("Deleted role %s", role.Name))
			})
		},
	}

	assignCmd := &cobra.Command{
		Use:   "assign <server> <member> <role>",
		Short: "Assign a role to a member",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleMember(app, cmd, args[0], args[1], args[2], true)
		},
	}
	removeCmd := &cobra.Command{
		Use:   "remove <server> <member> <role>",
		Short: "Remove a role from a member",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleMember(app, cmd, args[0], args[1], args[2], false)
		},
	}

	var rName, rColor string
	var rHoist, rNoHoist, rMention, rNoMention bool
	editCmd := &cobra.Command{
		Use:   "edit <server> <role>",
		Short: "Edit role settings",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rHoist && rNoHoist {
				return output.UsageError("cannot pass both --hoist and --no-hoist")
			}
			if rMention && rNoMention {
				return output.UsageError("cannot pass both --mentionable and --no-mentionable")
			}
			update := disgodiscord.RoleUpdate{}
			var changed []string
			if cmd.Flags().Changed("name") {
				update.Name = &rName
				changed = append(changed, "name")
			}
			if cmd.Flags().Changed("color") {
				c, err := voxdiscord.ParseHexColor(rColor)
				if err != nil {
					return err
				}
				update.Color = &c
				changed = append(changed, "color")
			}
			if rHoist {
				t := true
				update.Hoist = &t
				changed = append(changed, "hoist")
			} else if rNoHoist {
				f := false
				update.Hoist = &f
				changed = append(changed, "hoist")
			}
			if rMention {
				t := true
				update.Mentionable = &t
				changed = append(changed, "mentionable")
			} else if rNoMention {
				f := false
				update.Mentionable = &f
				changed = append(changed, "mentionable")
			}
			if len(changed) == 0 {
				return output.UsageError("no changes specified")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				role, err := voxdiscord.ResolveRole(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				updated, err := client.UpdateRole(guild.ID, role.ID, update)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": updated.ID.String(), "name": updated.Name, "updated": changed},
					fmt.Sprintf("Updated role %s: %s", updated.Name, strings.Join(changed, ", ")),
				)
			})
		},
	}
	editCmd.Flags().StringVar(&rName, "name", "", "New role name")
	editCmd.Flags().StringVar(&rColor, "color", "", "New hex color")
	editCmd.Flags().BoolVar(&rHoist, "hoist", false, "Display role separately in sidebar")
	editCmd.Flags().BoolVar(&rNoHoist, "no-hoist", false, "Do not display role separately")
	editCmd.Flags().BoolVar(&rMention, "mentionable", false, "Allow @mentioning role")
	editCmd.Flags().BoolVar(&rNoMention, "no-mentionable", false, "Disallow @mentioning role")

	group.AddCommand(listCmd, createCmd, deleteCmd, assignCmd, removeCmd, editCmd)
	return group
}

func runRoleMember(app *App, cmd *cobra.Command, serverRef, memberRef, roleRef string, assign bool) error {
	return app.WithRest(cmd, func(client rest.Rest) error {
		guild, err := voxdiscord.ResolveGuild(client, serverRef)
		if err != nil {
			return err
		}
		member, err := voxdiscord.ResolveMember(client, guild.ID, memberRef)
		if err != nil {
			return err
		}
		role, err := voxdiscord.ResolveRole(client, guild.ID, roleRef)
		if err != nil {
			return err
		}
		if assign {
			err = client.AddMemberRole(guild.ID, member.User.ID, role.ID)
		} else {
			err = client.RemoveMemberRole(guild.ID, member.User.ID, role.ID)
		}
		if err != nil {
			return voxdiscord.MapError(err)
		}
		tag := voxdiscord.FormatUserTag(member.User)
		verb := "Removed"
		prep := "from"
		if assign {
			verb = "Assigned"
			prep = "to"
		}
		return app.Out.Print(
			map[string]any{"member": tag, "role": role.Name},
			fmt.Sprintf("%s %s %s %s", verb, role.Name, prep, tag),
		)
	})
}

func newMemberCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "member",
		Short:   "List, inspect, nickname, timeout, kick, ban, and unban members",
		GroupID: "server",
	}

	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list <server>",
		Short: "List members of a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				members, err := voxdiscord.FetchGuildMembers(client, guild.ID, listLimit)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(members))
				lines := make([]string, 0, len(members))
				for _, m := range members {
					tag := voxdiscord.FormatUserTag(m.User)
					data = append(data, map[string]any{
						"id":   m.User.ID.String(),
						"name": tag,
						"nick": m.Nick,
						"bot":  m.User.Bot,
					})
					parts := []string{tag}
					if m.User.Bot {
						parts = append(parts, "(bot)")
					}
					if m.Nick != nil && *m.Nick != "" {
						parts = append(parts, "aka "+*m.Nick)
					}
					lines = append(lines, strings.Join(parts, " "))
				}
				plain := "No members found."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}
	listCmd.Flags().IntVar(&listLimit, "limit", 50, "Maximum members to list")

	infoCmd := &cobra.Command{
		Use:   "info <server> <member>",
		Short: "Show member details",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				m, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				rolesByID := make(map[snowflake.ID]string)
				if roles, err := client.GetRoles(guild.ID); err == nil {
					for _, r := range roles {
						rolesByID[r.ID] = r.Name
					}
				}
				roleNames := make([]string, 0, len(m.RoleIDs))
				for _, rid := range m.RoleIDs {
					if rid != guild.ID {
						roleNames = append(roleNames, rolesByID[rid])
					}
				}
				var joinedAt any
				if m.JoinedAt != nil {
					joinedAt = m.JoinedAt.UTC().Format(time.RFC3339)
				}
				tag := voxdiscord.FormatUserTag(m.User)
				data := map[string]any{
					"id":        m.User.ID.String(),
					"name":      tag,
					"nick":      m.Nick,
					"bot":       m.User.Bot,
					"roles":     roleNames,
					"joined_at": joinedAt,
				}
				lines := []string{
					fmt.Sprintf("id: %s", m.User.ID),
					fmt.Sprintf("name: %s", tag),
					fmt.Sprintf("bot: %v", m.User.Bot),
					fmt.Sprintf("roles: %s", noneOrJoin(roleNames)),
				}
				if m.Nick != nil {
					lines = append(lines, "nick: "+*m.Nick)
				}
				if joinedAt != nil {
					lines = append(lines, fmt.Sprintf("joined_at: %v", joinedAt))
				}
				return app.Out.Print(data, strings.Join(lines, "\n"))
			})
		},
	}

	var kickReason, kickTriggeredBy string
	kickCmd := &cobra.Command{
		Use:   "kick <server> <member>",
		Short: "Kick a member from the server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("member kick", fmt.Sprintf("%s from %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if err := checkTriggeredBy(app, client, guild, kickTriggeredBy, "kick"); err != nil {
					return err
				}
				member, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				var opts []rest.RequestOpt
				if kickReason != "" {
					opts = append(opts, rest.WithReason(kickReason))
				}
				if err := client.RemoveMember(guild.ID, member.User.ID, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(member.User)
				_ = app.Security.Audit.Record("member kick", map[string]any{"server": args[0], "member": tag, "reason": kickReason}, "ok", kickTriggeredBy)
				return app.Out.Print(map[string]any{"member": tag, "kicked": true}, fmt.Sprintf("Kicked %s", tag))
			})
		},
	}
	kickCmd.Flags().StringVar(&kickReason, "reason", "", "Reason for kick")
	kickCmd.Flags().StringVar(&kickTriggeredBy, "triggered-by", "", "User ID who triggered this action (for permission check)")

	var banReason, banTriggeredBy string
	banCmd := &cobra.Command{
		Use:   "ban <server> <member>",
		Short: "Ban a member from the server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("member ban", fmt.Sprintf("%s from %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if err := checkTriggeredBy(app, client, guild, banTriggeredBy, "ban"); err != nil {
					return err
				}
				member, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				var opts []rest.RequestOpt
				if banReason != "" {
					opts = append(opts, rest.WithReason(banReason))
				}
				if err := client.AddBan(guild.ID, member.User.ID, 0, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(member.User)
				_ = app.Security.Audit.Record("member ban", map[string]any{"server": args[0], "member": tag, "reason": banReason}, "ok", banTriggeredBy)
				return app.Out.Print(map[string]any{"member": tag, "banned": true}, fmt.Sprintf("Banned %s", tag))
			})
		},
	}
	banCmd.Flags().StringVar(&banReason, "reason", "", "Reason for ban")
	banCmd.Flags().StringVar(&banTriggeredBy, "triggered-by", "", "User ID who triggered this action (for permission check)")

	var unbanTriggeredBy string
	unbanCmd := &cobra.Command{
		Use:   "unban <server> <member>",
		Short: "Unban a user from the server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("member unban", fmt.Sprintf("%s from %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if err := checkTriggeredBy(app, client, guild, unbanTriggeredBy, "ban"); err != nil {
					return err
				}
				bans, err := client.GetBans(guild.ID, 0, 0, 1000)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				var target *disgodiscord.User
				for i := range bans {
					u := bans[i].User
					if u.ID.String() == args[1] || strings.EqualFold(u.Username, args[1]) || strings.EqualFold(voxdiscord.FormatUserTag(u), args[1]) {
						target = &u
						break
					}
				}
				if target == nil {
					return output.NewError("not_found", fmt.Sprintf("Banned user not found: %s", args[1]))
				}
				if err := client.DeleteBan(guild.ID, target.ID); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(*target)
				_ = app.Security.Audit.Record("member unban", map[string]any{"server": args[0], "member": tag}, "ok", unbanTriggeredBy)
				return app.Out.Print(map[string]any{"member": tag, "unbanned": true}, fmt.Sprintf("Unbanned %s", tag))
			})
		},
	}
	unbanCmd.Flags().StringVar(&unbanTriggeredBy, "triggered-by", "", "User ID who triggered this action (for permission check)")

	var timeoutReason, timeoutTriggeredBy string
	timeoutCmd := &cobra.Command{
		Use:   "timeout <server> <member> <duration_seconds>",
		Short: "Timeout a member for N seconds (0 clears timeout, max 2419200)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			duration, err := strconv.Atoi(args[2])
			if err != nil || duration < 0 || duration > 2419200 {
				return output.UsageError("duration must be an integer between 0 and 2419200 seconds (28 days)")
			}
			if err := app.ConfirmDestructive("member timeout", fmt.Sprintf("%s in %s for %ds", args[1], args[0], duration)); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if err := checkTriggeredBy(app, client, guild, timeoutTriggeredBy, "moderate_members"); err != nil {
					return err
				}
				member, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				var until *time.Time
				if duration > 0 {
					t := time.Now().UTC().Add(time.Duration(duration) * time.Second)
					until = &t
				}
				var opts []rest.RequestOpt
				if timeoutReason != "" {
					opts = append(opts, rest.WithReason(timeoutReason))
				}
				if _, err := client.UpdateMember(guild.ID, member.User.ID, disgodiscord.MemberUpdate{
					CommunicationDisabledUntil: omit.New(until),
				}, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(member.User)
				if duration == 0 {
					_ = app.Security.Audit.Record("member timeout_remove", map[string]any{"server": args[0], "member": tag}, "ok", timeoutTriggeredBy)
					return app.Out.Print(map[string]any{"member": tag, "timeout_removed": true}, fmt.Sprintf("Removed timeout from %s", tag))
				}
				_ = app.Security.Audit.Record("member timeout", map[string]any{"server": args[0], "member": tag, "duration": duration, "reason": timeoutReason}, "ok", timeoutTriggeredBy)
				return app.Out.Print(map[string]any{"member": tag, "timeout_seconds": duration}, fmt.Sprintf("Timed out %s for %ds", tag, duration))
			})
		},
	}
	timeoutCmd.Flags().StringVar(&timeoutReason, "reason", "", "Reason for timeout")
	timeoutCmd.Flags().StringVar(&timeoutTriggeredBy, "triggered-by", "", "User ID who triggered this action")

	var nickClear bool
	var nickReason, nickTriggeredBy string
	nickCmd := &cobra.Command{
		Use:   "nick <server> <member> [nickname]",
		Short: "Set or clear a member's server nickname",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			hasNick := len(args) == 3
			if nickClear && hasNick {
				return output.UsageError("pass either a nickname or --clear, not both")
			}
			if !nickClear && !hasNick {
				return output.UsageError("provide a nickname, or use --clear to remove it")
			}
			newNick := ""
			if hasNick {
				newNick = args[2]
				if len(newNick) > 32 {
					return output.UsageError("nicknames cannot exceed 32 characters")
				}
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				if err := checkTriggeredBy(app, client, guild, nickTriggeredBy, "manage_nicknames"); err != nil {
					return err
				}
				member, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				previous := member.Nick
				var opts []rest.RequestOpt
				if nickReason != "" {
					opts = append(opts, rest.WithReason(nickReason))
				}
				if _, err := client.UpdateMember(guild.ID, member.User.ID, disgodiscord.MemberUpdate{
					Nick: &newNick,
				}, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(member.User)
				var nickVal any
				plain := fmt.Sprintf("Cleared nickname for %s", tag)
				if !nickClear {
					nickVal = newNick
					plain = fmt.Sprintf("Set nickname for %s to '%s'", tag, newNick)
				}
				_ = app.Security.Audit.Record("member nick", map[string]any{"server": args[0], "member": tag, "from": previous, "to": nickVal}, "ok", nickTriggeredBy)
				return app.Out.Print(map[string]any{"member": tag, "previous_nick": previous, "nick": nickVal}, plain)
			})
		},
	}
	nickCmd.Flags().BoolVar(&nickClear, "clear", false, "Clear the member's nickname")
	nickCmd.Flags().StringVar(&nickReason, "reason", "", "Audit log reason")
	nickCmd.Flags().StringVar(&nickTriggeredBy, "triggered-by", "", "User ID who triggered this action")

	group.AddCommand(listCmd, infoCmd, kickCmd, banCmd, unbanCmd, timeoutCmd, nickCmd)
	return group
}

func checkTriggeredBy(app *App, client rest.Rest, guild *disgodiscord.RestGuild, triggeredBy, required string) error {
	if strings.TrimSpace(triggeredBy) == "" {
		return nil
	}
	uid, ok := voxdiscord.ParseSnowflake(triggeredBy)
	if !ok {
		return output.UsageError(fmt.Sprintf("invalid --triggered-by user ID: %s", triggeredBy))
	}
	verified, userLabel, err := voxdiscord.CheckTriggeredByPermission(client, guild, uid, required)
	if err != nil {
		_ = app.Security.Audit.Record("permission_check", map[string]any{
			"user_id": triggeredBy, "user": userLabel, "permission": required, "result": "denied",
		}, "denied", triggeredBy)
		return err
	}
	if !verified {
		app.Out.Note("Warning: Could not verify permissions for user %s. Proceeding anyway.", triggeredBy)
		_ = app.Security.Audit.Record("permission_check", map[string]any{
			"user_id": triggeredBy, "permission": required, "result": "skipped_not_found",
		}, "skipped", triggeredBy)
	}
	return nil
}

func loadDiscordIcon(path string) (*disgodiscord.Icon, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image %s: %w", path, err)
	}
	iconType := disgodiscord.IconTypePNG
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		iconType = disgodiscord.IconTypeJPEG
	case strings.HasSuffix(lower, ".gif"):
		iconType = disgodiscord.IconTypeGIF
	case strings.HasSuffix(lower, ".webp"):
		iconType = disgodiscord.IconTypeWEBP
	}
	return disgodiscord.NewIconRaw(iconType, raw), nil
}

func noneOrJoin(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func parseAuditAction(raw string) (disgodiscord.AuditLogEvent, bool) {
	if num, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return disgodiscord.AuditLogEvent(num), true
	}
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "-", "_")
	table := map[string]disgodiscord.AuditLogEvent{
		"guild_update":        disgodiscord.AuditLogEventGuildUpdate,
		"channel_create":      disgodiscord.AuditLogEventChannelCreate,
		"channel_update":      disgodiscord.AuditLogEventChannelUpdate,
		"channel_delete":      disgodiscord.AuditLogEventChannelDelete,
		"member_kick":         disgodiscord.AuditLogEventMemberKick,
		"member_ban_add":      disgodiscord.AuditLogEventMemberBanAdd,
		"member_ban_remove":   disgodiscord.AuditLogEventMemberBanRemove,
		"member_update":       disgodiscord.AuditLogEventMemberUpdate,
		"member_role_update":  disgodiscord.AuditLogEventMemberRoleUpdate,
		"role_create":         disgodiscord.AuditLogEventRoleCreate,
		"role_update":         disgodiscord.AuditLogEventRoleUpdate,
		"role_delete":         disgodiscord.AuditLogEventRoleDelete,
		"invite_create":       disgodiscord.AuditLogEventInviteCreate,
		"invite_delete":       disgodiscord.AuditLogEventInviteDelete,
		"webhook_create":      disgodiscord.AuditLogEventWebhookCreate,
		"webhook_delete":      disgodiscord.AuditLogEventWebhookDelete,
		"emoji_create":        disgodiscord.AuditLogEventEmojiCreate,
		"emoji_update":        disgodiscord.AuditLogEventEmojiUpdate,
		"emoji_delete":        disgodiscord.AuditLogEventEmojiDelete,
		"message_delete":      disgodiscord.AuditLogEventMessageDelete,
		"message_bulk_delete": disgodiscord.AuditLogEventMessageBulkDelete,
		"message_pin":         disgodiscord.AuditLogEventMessagePin,
		"message_unpin":       disgodiscord.AuditLogEventMessageUnpin,
		"thread_create":       disgodiscord.AuditLogThreadCreate,
		"thread_update":       disgodiscord.AuditLogThreadUpdate,
		"thread_delete":       disgodiscord.AuditLogThreadDelete,
	}
	action, ok := table[normalized]
	return action, ok
}
