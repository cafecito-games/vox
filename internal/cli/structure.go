package cli

import (
	"fmt"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/serverspec"
)

func newChannelCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "channel",
		Short:   "List, create, edit, delete, and manage permissions on channels",
		GroupID: "server",
	}

	// channel list
	var listServer string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List visible channels across all servers or one server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				type guildSummary struct {
					id   snowflake.ID
					name string
				}
				var guilds []guildSummary
				if listServer != "" {
					g, err := voxdiscord.ResolveGuild(client, listServer)
					if err != nil {
						return err
					}
					guilds = []guildSummary{{id: g.ID, name: g.Name}}
				} else {
					all, err := voxdiscord.FetchAllGuilds(client)
					if err != nil {
						return err
					}
					for _, g := range all {
						guilds = append(guilds, guildSummary{id: g.ID, name: g.Name})
					}
				}

				data := make([]map[string]any, 0)
				lines := make([]string, 0)
				for _, g := range guilds {
					channels, err := client.GetGuildChannels(g.id)
					if err != nil {
						continue
					}
					for _, ch := range channels {
						switch ch.Type() {
						case disgodiscord.ChannelTypeGuildText, disgodiscord.ChannelTypeGuildVoice, disgodiscord.ChannelTypeGuildForum:
							kind := serverspec.ChannelTypeName(ch.Type())
							data = append(data, map[string]any{
								"id":     ch.ID().String(),
								"name":   ch.Name(),
								"type":   kind,
								"server": g.name,
							})
							lines = append(lines, fmt.Sprintf("#%s (%s) — %s (ID: %s)", ch.Name(), kind, g.name, ch.ID()))
						}
					}
				}
				plain := "No channels found."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				err := app.Out.Print(data, plain)
				app.Out.WarnChannelVisibility()
				return err
			})
		},
	}
	listCmd.Flags().StringVar(&listServer, "server", "", "Filter by server name or ID")

	// channel create
	var createType, createTopic string
	createCmd := &cobra.Command{
		Use:   "create <server> <name>",
		Short: "Create a text, voice, category, or forum channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				var payload disgodiscord.GuildChannelCreate
				switch strings.ToLower(createType) {
				case "text":
					payload = disgodiscord.GuildTextChannelCreate{Name: args[1], Topic: createTopic}
				case "voice":
					payload = disgodiscord.GuildVoiceChannelCreate{Name: args[1]}
				case "category":
					payload = disgodiscord.GuildCategoryChannelCreate{Name: args[1]}
				case "forum":
					payload = disgodiscord.GuildForumChannelCreate{Name: args[1], Topic: createTopic}
				default:
					return output.UsageError(fmt.Sprintf("invalid --type %q (use text, voice, category, forum)", createType))
				}
				ch, err := client.CreateGuildChannel(guild.ID, payload)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				kind := serverspec.ChannelTypeName(ch.Type())
				return app.Out.Print(
					map[string]any{"id": ch.ID().String(), "name": ch.Name(), "type": kind},
					fmt.Sprintf("Created #%s (ID: %s)", ch.Name(), ch.ID()),
				)
			})
		},
	}
	createCmd.Flags().StringVar(&createType, "type", "text", "Channel type: text, voice, category, or forum")
	createCmd.Flags().StringVar(&createTopic, "topic", "", "Channel topic (text/forum)")

	// channel delete
	deleteCmd := &cobra.Command{
		Use:   "delete <channel>",
		Short: "Delete a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("channel delete", args[0]); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				name := ch.Name()
				if err := client.DeleteChannel(ch.ID()); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("channel delete", map[string]any{"channel": name}, "ok", "")
				return app.Out.Print(map[string]any{"id": ch.ID().String(), "deleted": true}, fmt.Sprintf("Deleted #%s", name))
			})
		},
	}

	// channel info
	infoCmd := &cobra.Command{
		Use:   "info <channel>",
		Short: "Show channel details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				serverName := ""
				if gch, ok := ch.(disgodiscord.GuildChannel); ok {
					if g, err := client.GetGuild(gch.GuildID(), false); err == nil {
						serverName = g.Name
					}
				}
				var topic any
				if tch, ok := ch.(disgodiscord.GuildTextChannel); ok && tch.Topic() != nil {
					topic = *tch.Topic()
				}
				kind := serverspec.ChannelTypeName(ch.Type())
				createdAt := ch.ID().Time().UTC().Format(time.RFC3339)
				data := map[string]any{
					"id":         ch.ID().String(),
					"name":       ch.Name(),
					"type":       kind,
					"server":     serverName,
					"topic":      topic,
					"created_at": createdAt,
				}
				lines := []string{
					fmt.Sprintf("id: %s", ch.ID()),
					fmt.Sprintf("name: %s", ch.Name()),
					fmt.Sprintf("type: %s", kind),
					fmt.Sprintf("server: %s", serverName),
					fmt.Sprintf("created_at: %s", createdAt),
				}
				if topic != nil {
					lines = append(lines, fmt.Sprintf("topic: %v", topic))
				}
				return app.Out.Print(data, strings.Join(lines, "\n"))
			})
		},
	}

	// channel edit
	var editName, editTopic string
	var editSlowmode int
	var editNSFW, editNoNSFW bool
	editCmd := &cobra.Command{
		Use:   "edit <channel>",
		Short: "Edit channel name, topic, slowmode, or NSFW setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if editNSFW && editNoNSFW {
				return output.UsageError("cannot pass both --nsfw and --no-nsfw")
			}
			update := disgodiscord.GuildTextChannelUpdate{}
			var changed []string
			if cmd.Flags().Changed("name") {
				update.Name = &editName
				changed = append(changed, "name")
			}
			if cmd.Flags().Changed("topic") {
				update.Topic = &editTopic
				changed = append(changed, "topic")
			}
			if cmd.Flags().Changed("slowmode") {
				update.RateLimitPerUser = &editSlowmode
				changed = append(changed, "slowmode_delay")
			}
			if editNSFW {
				t := true
				update.NSFW = &t
				changed = append(changed, "nsfw")
			} else if editNoNSFW {
				f := false
				update.NSFW = &f
				changed = append(changed, "nsfw")
			}
			if len(changed) == 0 {
				return output.UsageError("no changes specified")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				updated, err := client.UpdateChannel(ch.ID(), update)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": updated.ID().String(), "name": updated.Name(), "updated": changed},
					fmt.Sprintf("Updated #%s: %s", updated.Name(), strings.Join(changed, ", ")),
				)
			})
		},
	}
	editCmd.Flags().StringVar(&editName, "name", "", "New channel name")
	editCmd.Flags().StringVar(&editTopic, "topic", "", "New channel topic")
	editCmd.Flags().IntVar(&editSlowmode, "slowmode", 0, "Slowmode delay in seconds (0 to disable)")
	editCmd.Flags().BoolVar(&editNSFW, "nsfw", false, "Mark channel NSFW")
	editCmd.Flags().BoolVar(&editNoNSFW, "no-nsfw", false, "Mark channel non-NSFW")

	// channel forum-post
	var forumFiles []string
	forumPostCmd := &cobra.Command{
		Use:   "forum-post <channel> <title> <content>",
		Short: "Create a forum post in a forum channel",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				if ch.Type() != disgodiscord.ChannelTypeGuildForum {
					return output.UsageError(fmt.Sprintf("#%s is not a forum channel", ch.Name()))
				}
				files, cleanup, err := voxdiscord.OpenFiles(forumFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				post, err := client.CreatePostInThreadChannel(ch.ID(), disgodiscord.ThreadChannelPostCreate{
					Name:    args[1],
					Message: disgodiscord.MessageCreate{Content: args[2], Files: files},
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{
						"thread_id":   post.ID().String(),
						"thread_name": post.Name(),
						"message_id":  post.Message.ID.String(),
						"channel":     ch.Name(),
					},
					fmt.Sprintf("Created forum post '%s' in #%s", args[1], ch.Name()),
				)
			})
		},
	}
	forumPostCmd.Flags().StringArrayVar(&forumFiles, "file", nil, "File path to attach (repeatable)")

	// channel set-permissions
	var permAllow, permDeny, permTargetType string
	setPermsCmd := &cobra.Command{
		Use:   "set-permissions <channel> <target>",
		Short: "Set permission overwrites for a role or member on a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			allowNames := splitCSV(permAllow)
			denyNames := splitCSV(permDeny)
			allowMask, err := voxdiscord.ParsePermissionNames(allowNames)
			if err != nil {
				return err
			}
			denyMask, err := voxdiscord.ParsePermissionNames(denyNames)
			if err != nil {
				return err
			}
			// Deny wins if the same permission is in both lists.
			allowMask = allowMask.Remove(denyMask)

			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				gch, ok := ch.(disgodiscord.GuildChannel)
				if !ok {
					return output.UsageError("permissions can only be set on server channels")
				}
				var targetID snowflake.ID
				var targetLabel string
				var overwrite disgodiscord.PermissionOverwriteUpdate
				switch strings.ToLower(permTargetType) {
				case "role":
					role, err := voxdiscord.ResolveRole(client, gch.GuildID(), args[1])
					if err != nil {
						return err
					}
					targetID = role.ID
					targetLabel = role.Name
					overwrite = disgodiscord.RolePermissionOverwriteUpdate{Allow: &allowMask, Deny: &denyMask}
				case "member":
					member, err := voxdiscord.ResolveMember(client, gch.GuildID(), args[1])
					if err != nil {
						return err
					}
					targetID = member.User.ID
					targetLabel = voxdiscord.FormatUserTag(member.User)
					overwrite = disgodiscord.MemberPermissionOverwriteUpdate{Allow: &allowMask, Deny: &denyMask}
				default:
					return output.UsageError("invalid --target-type (use role or member)")
				}
				if err := client.UpdatePermissionOverwrite(gch.ID(), targetID, overwrite); err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"channel": ch.Name(), "target": targetLabel, "allow": permAllow, "deny": permDeny},
					fmt.Sprintf("Updated permissions for %s on #%s", targetLabel, ch.Name()),
				)
			})
		},
	}
	setPermsCmd.Flags().StringVar(&permAllow, "allow", "", "Comma-separated permissions to allow")
	setPermsCmd.Flags().StringVar(&permDeny, "deny", "", "Comma-separated permissions to deny")
	setPermsCmd.Flags().StringVar(&permTargetType, "target-type", "role", "Target type: role or member")

	group.AddCommand(listCmd, createCmd, deleteCmd, infoCmd, editCmd, forumPostCmd, setPermsCmd)
	return group
}

func newThreadCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "thread",
		Short:   "Create, list, archive, rename, message, and manage thread members",
		GroupID: "server",
	}

	createCmd := &cobra.Command{
		Use:   "create <channel> <message_id> <name>",
		Short: "Create a thread from a message",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid message ID: %s", args[1]))
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				thread, err := client.CreateThreadFromMessage(ch.ID(), msgID, disgodiscord.ThreadCreateFromMessage{
					Name: args[2],
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{
						"id":                thread.ID().String(),
						"name":              thread.Name(),
						"parent_channel":    ch.Name(),
						"parent_channel_id": ch.ID().String(),
						"message_id":        args[1],
					},
					fmt.Sprintf("Created thread '%s' (ID: %s) from message %s", thread.Name(), thread.ID(), args[1]),
				)
			})
		},
	}

	listCmd := &cobra.Command{
		Use:   "list <channel>",
		Short: "List active threads in a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				gch, ok := ch.(disgodiscord.GuildChannel)
				if !ok {
					return output.UsageError("channel is not a server channel")
				}
				active, err := client.GetActiveGuildThreads(gch.GuildID())
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0)
				lines := make([]string, 0)
				for _, t := range active.Threads {
					if t.ParentID() == nil || *t.ParentID() != ch.ID() {
						continue
					}
					data = append(data, map[string]any{
						"id":            t.ID().String(),
						"name":          t.Name(),
						"message_count": t.MessageCount,
						"member_count":  t.MemberCount,
						"archived":      t.ThreadMetadata.Archived,
					})
					line := fmt.Sprintf("%s (ID: %s, msgs: %d, members: %d)", t.Name(), t.ID(), t.MessageCount, t.MemberCount)
					if t.ThreadMetadata.Archived {
						line += " [archived]"
					}
					lines = append(lines, line)
				}
				plain := "No active threads."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var threadSendFiles []string
	sendCmd := &cobra.Command{
		Use:   "send <thread> <text>",
		Short: "Send a message inside a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				thread, err := voxdiscord.ResolveThread(client, args[0])
				if err != nil {
					return err
				}
				files, cleanup, err := voxdiscord.OpenFiles(threadSendFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				msg, err := client.CreateMessage(thread.ID(), disgodiscord.MessageCreate{
					Content: args[1],
					Files:   files,
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := map[string]any{
					"id":        msg.ID.String(),
					"thread":    thread.Name(),
					"thread_id": thread.ID().String(),
					"content":   msg.Content,
				}
				if len(msg.Attachments) > 0 {
					data["attachments"] = serializeAttachments(msg.Attachments)
				}
				return app.Out.Print(data, fmt.Sprintf("Sent message %s to thread '%s'", msg.ID, thread.Name()))
			})
		},
	}
	sendCmd.Flags().StringArrayVar(&threadSendFiles, "file", nil, "File path to attach (repeatable)")

	archiveCmd := &cobra.Command{
		Use:   "archive <thread>",
		Short: "Archive a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runThreadArchive(app, cmd, args[0], true)
		},
	}
	unarchiveCmd := &cobra.Command{
		Use:   "unarchive <thread>",
		Short: "Unarchive a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runThreadArchive(app, cmd, args[0], false)
		},
	}

	renameCmd := &cobra.Command{
		Use:   "rename <thread> <new_name>",
		Short: "Rename a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				thread, err := voxdiscord.ResolveThread(client, args[0])
				if err != nil {
					return err
				}
				oldName := thread.Name()
				newName := args[1]
				if _, err := client.UpdateChannel(thread.ID(), disgodiscord.GuildThreadUpdate{Name: &newName}); err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": thread.ID().String(), "old_name": oldName, "new_name": newName},
					fmt.Sprintf("Renamed thread '%s' to '%s'", oldName, newName),
				)
			})
		},
	}

	addMemberCmd := &cobra.Command{
		Use:   "add-member <thread> <member_id>",
		Short: "Add a member to a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runThreadMember(app, cmd, args[0], args[1], true)
		},
	}
	removeMemberCmd := &cobra.Command{
		Use:   "remove-member <thread> <member_id>",
		Short: "Remove a member from a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runThreadMember(app, cmd, args[0], args[1], false)
		},
	}

	group.AddCommand(createCmd, listCmd, sendCmd, archiveCmd, unarchiveCmd, renameCmd, addMemberCmd, removeMemberCmd)
	return group
}

func runThreadArchive(app *App, cmd *cobra.Command, threadRef string, archived bool) error {
	return app.WithRest(cmd, func(client rest.Rest) error {
		thread, err := voxdiscord.ResolveThread(client, threadRef)
		if err != nil {
			return err
		}
		if _, err := client.UpdateChannel(thread.ID(), disgodiscord.GuildThreadUpdate{Archived: &archived}); err != nil {
			return voxdiscord.MapError(err)
		}
		verb := "Unarchived"
		if archived {
			verb = "Archived"
		}
		return app.Out.Print(map[string]any{"id": thread.ID().String(), "archived": archived}, fmt.Sprintf("%s thread '%s'", verb, thread.Name()))
	})
}

func runThreadMember(app *App, cmd *cobra.Command, threadRef, memberIDRaw string, add bool) error {
	memberID, ok := voxdiscord.ParseSnowflake(memberIDRaw)
	if !ok {
		return output.UsageError(fmt.Sprintf("invalid member ID: %s", memberIDRaw))
	}
	return app.WithRest(cmd, func(client rest.Rest) error {
		thread, err := voxdiscord.ResolveThread(client, threadRef)
		if err != nil {
			return err
		}
		if add {
			err = client.AddThreadMember(thread.ID(), memberID)
		} else {
			err = client.RemoveThreadMember(thread.ID(), memberID)
		}
		if err != nil {
			return voxdiscord.MapError(err)
		}
		verb := "Removed"
		prep := "from"
		if add {
			verb = "Added"
			prep = "to"
		}
		return app.Out.Print(
			map[string]any{"thread_id": thread.ID().String(), "member": memberID.String()},
			fmt.Sprintf("%s %s %s thread '%s'", verb, memberID, prep, thread.Name()),
		)
	})
}

func newPollCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "poll",
		Short:   "Create, inspect, and end native Discord polls",
		GroupID: "messaging",
	}

	var pollDuration int
	var pollMultiple bool
	var pollEmojis []string
	createCmd := &cobra.Command{
		Use:   "create <channel> <question> <answer1> <answer2> [answer...]",
		Short: "Create a poll in a channel",
		Args:  cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			channelRef, question, answers := args[0], args[1], args[2:]
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, channelRef)
				if err != nil {
					return err
				}
				pollAnswers := make([]disgodiscord.PollMedia, 0, len(answers))
				for i, ans := range answers {
					text := ans
					media := disgodiscord.PollMedia{Text: &text}
					if i < len(pollEmojis) && pollEmojis[i] != "" {
						e := pollEmojis[i]
						media.Emoji = &disgodiscord.PartialEmoji{Name: &e}
					}
					pollAnswers = append(pollAnswers, media)
				}
				q := question
				msg, err := client.CreateMessage(ch.ID(), disgodiscord.MessageCreate{
					Poll: &disgodiscord.PollCreate{
						Question:         disgodiscord.PollMedia{Text: &q},
						Answers:          pollAnswers,
						Duration:         pollDuration,
						AllowMultiselect: pollMultiple,
					},
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := map[string]any{
					"message_id":     msg.ID.String(),
					"channel":        ch.Name(),
					"channel_id":     ch.ID().String(),
					"question":       question,
					"answers":        answers,
					"duration_hours": pollDuration,
					"multiple":       pollMultiple,
				}
				if len(pollEmojis) > 0 {
					data["emojis"] = pollEmojis
				}
				return app.Out.Print(data, fmt.Sprintf("Poll created in #%s (message %s): %s", ch.Name(), msg.ID, question))
			})
		},
	}
	createCmd.Flags().IntVar(&pollDuration, "duration", 24, "Poll duration in hours")
	createCmd.Flags().BoolVar(&pollMultiple, "multiple", false, "Allow multiple selections")
	createCmd.Flags().StringArrayVarP(&pollEmojis, "emoji", "e", nil, "Emoji for each answer in order (repeatable)")

	resultsCmd := &cobra.Command{
		Use:   "results <channel> <message_id>",
		Short: "View poll vote totals",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid message ID: %s", args[1]))
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				msg, err := client.GetMessage(ch.ID(), msgID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				if msg.Poll == nil {
					return output.NewError("no_poll", "Message has no poll")
				}
				counts := make(map[int]int)
				total := 0
				finalized := false
				if msg.Poll.Results != nil {
					finalized = msg.Poll.Results.IsFinalized
					for _, ac := range msg.Poll.Results.AnswerCounts {
						counts[ac.ID] = ac.Count
						total += ac.Count
					}
				}
				q := ""
				if msg.Poll.Question.Text != nil {
					q = *msg.Poll.Question.Text
				}
				ansData := make([]map[string]any, 0, len(msg.Poll.Answers))
				lines := []string{fmt.Sprintf("Poll: %s (votes: %d)", q, total)}
				for _, a := range msg.Poll.Answers {
					id := 0
					if a.AnswerID != nil {
						id = *a.AnswerID
					}
					text := ""
					if a.PollMedia.Text != nil {
						text = *a.PollMedia.Text
					}
					ansData = append(ansData, map[string]any{"id": id, "text": text, "vote_count": counts[id]})
					lines = append(lines, fmt.Sprintf("  %s: %d votes", text, counts[id]))
				}
				return app.Out.Print(
					map[string]any{"question": q, "total_votes": total, "is_finalised": finalized, "answers": ansData},
					strings.Join(lines, "\n"),
				)
			})
		},
	}

	endCmd := &cobra.Command{
		Use:   "end <channel> <message_id>",
		Short: "End a poll early",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid message ID: %s", args[1]))
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				if _, err := client.ExpirePoll(ch.ID(), msgID); err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(map[string]any{"ended": true}, fmt.Sprintf("Ended poll on message %s", args[1]))
			})
		},
	}

	group.AddCommand(createCmd, resultsCmd, endCmd)
	return group
}

func newWebhookCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "webhook",
		Short:   "List, create, send through, and delete channel webhooks",
		GroupID: "server",
	}

	listCmd := &cobra.Command{
		Use:   "list <channel>",
		Short: "List webhooks on a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				hooks, err := client.GetWebhooks(ch.ID())
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(hooks))
				lines := make([]string, 0, len(hooks))
				for _, h := range hooks {
					urlStr := ""
					if inc, ok := h.(disgodiscord.IncomingWebhook); ok {
						urlStr = inc.URL()
					}
					data = append(data, map[string]any{"id": h.ID().String(), "name": h.Name(), "url": urlStr})
					lines = append(lines, fmt.Sprintf("%s (ID: %s)", h.Name(), h.ID()))
				}
				plain := "No webhooks."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	createCmd := &cobra.Command{
		Use:   "create <channel> <name>",
		Short: "Create a webhook on a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				hook, err := client.CreateWebhook(ch.ID(), disgodiscord.WebhookCreate{Name: args[1]})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"id": hook.ID().String(), "name": hook.Name(), "url": hook.URL()},
					fmt.Sprintf("Created webhook '%s' (URL: %s)", hook.Name(), hook.URL()),
				)
			})
		},
	}

	var whUser, whAvatar, whThread, whEmbedTitle, whEmbedDesc, whEmbedColor string
	var whFiles []string
	sendCmd := &cobra.Command{
		Use:   "send <channel> <webhook> <text>",
		Short: "Post a message via a webhook under an optional custom identity",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				hook, err := voxdiscord.ResolveWebhook(client, ch.ID(), args[1])
				if err != nil {
					return err
				}
				inc, ok := hook.(disgodiscord.IncomingWebhook)
				if !ok || inc.Token == "" {
					return output.NewError("webhook_no_token", "webhook does not carry an execution token")
				}
				payload := disgodiscord.WebhookMessageCreate{
					Content:   args[2],
					Username:  whUser,
					AvatarURL: whAvatar,
				}
				embedSpec := voxdiscord.EmbedSpec{
					Title:       whEmbedTitle,
					Description: whEmbedDesc,
					Color:       whEmbedColor,
				}
				if !embedSpec.IsEmpty() {
					embed, err := voxdiscord.BuildEmbed(embedSpec)
					if err != nil {
						return err
					}
					payload.Embeds = []disgodiscord.Embed{embed}
				}
				files, cleanup, err := voxdiscord.OpenFiles(whFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				payload.Files = files

				var threadID snowflake.ID
				if whThread != "" {
					var valid bool
					threadID, valid = voxdiscord.ParseSnowflake(whThread)
					if !valid {
						return output.UsageError(fmt.Sprintf("invalid --thread ID: %s", whThread))
					}
				}
				msg, err := client.CreateWebhookMessage(inc.ID(), inc.Token, payload, rest.CreateWebhookMessageParams{
					Wait:     true,
					ThreadID: threadID,
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				displayUser := whUser
				if displayUser == "" {
					displayUser = inc.Name()
				}
				return app.Out.Print(
					map[string]any{
						"id":         msg.ID.String(),
						"webhook":    inc.Name(),
						"webhook_id": inc.ID().String(),
						"channel":    ch.Name(),
						"content":    msg.Content,
						"username":   displayUser,
					},
					fmt.Sprintf("Sent message %s via webhook '%s'", msg.ID, inc.Name()),
				)
			})
		},
	}
	sendCmd.Flags().StringVar(&whUser, "username", "", "Override display name for this message")
	sendCmd.Flags().StringVar(&whAvatar, "avatar-url", "", "Override avatar URL for this message")
	sendCmd.Flags().StringVar(&whThread, "thread", "", "Thread ID to post into")
	sendCmd.Flags().StringVar(&whEmbedTitle, "embed-title", "", "Embed title")
	sendCmd.Flags().StringVar(&whEmbedDesc, "embed-desc", "", "Embed description")
	sendCmd.Flags().StringVar(&whEmbedColor, "embed-color", "", "Embed hex color")
	sendCmd.Flags().StringArrayVar(&whFiles, "file", nil, "File path to attach (repeatable)")

	deleteCmd := &cobra.Command{
		Use:   "delete <channel> <webhook>",
		Short: "Delete a webhook",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ConfirmDestructive("webhook delete", "webhook "+args[1]); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				hook, err := voxdiscord.ResolveWebhook(client, ch.ID(), args[1])
				if err != nil {
					return err
				}
				if err := client.DeleteWebhook(hook.ID()); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("webhook delete", map[string]any{"channel": args[0], "webhook_id": hook.ID().String()}, "ok", "")
				return app.Out.Print(map[string]any{"id": hook.ID().String(), "deleted": true}, fmt.Sprintf("Deleted webhook '%s'", hook.Name()))
			})
		},
	}

	group.AddCommand(listCmd, createCmd, sendCmd, deleteCmd, newWebhookPostCommand(app))
	return group
}

func splitCSV(raw string) []string {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
