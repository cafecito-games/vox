package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

func newMessageCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "message",
		Short:   "Send, list, history, search, edit, reply, pin, and delete messages",
		GroupID: "messaging",
	}

	// message send
	var (
		embedTitle, embedDesc, embedColor, embedFooter, embedImage, embedThumb, embedAuthor string
		embedFields, sendFiles                                                              []string
	)
	sendCmd := &cobra.Command{
		Use:   "send <channel> <text>",
		Short: "Send a message to a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			channelRef, text := args[0], args[1]
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, channelRef)
				if err != nil {
					return err
				}
				create := disgodiscord.MessageCreate{Content: text}
				embedSpec := voxdiscord.EmbedSpec{
					Title:       embedTitle,
					Description: embedDesc,
					Color:       embedColor,
					Footer:      embedFooter,
					Image:       embedImage,
					Thumbnail:   embedThumb,
					Author:      embedAuthor,
				}
				if embedFooter == "" {
					embedSpec.Footer = nil
				}
				if embedAuthor == "" {
					embedSpec.Author = nil
				}
				for _, rawField := range embedFields {
					parsed, err := voxdiscord.ParseEmbedFieldFlag(rawField)
					if err != nil {
						return err
					}
					embedSpec.Fields = append(embedSpec.Fields, parsed)
				}
				if !embedSpec.IsEmpty() {
					embed, err := voxdiscord.BuildEmbed(embedSpec)
					if err != nil {
						return err
					}
					create.Embeds = []disgodiscord.Embed{embed}
				}
				files, cleanup, err := voxdiscord.OpenFiles(sendFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				create.Files = files

				msg, err := client.CreateMessage(ch.ID(), create)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := map[string]any{
					"id":       msg.ID.String(),
					"channel":  ch.Name(),
					"content":  msg.Content,
					"jump_url": msg.JumpURL(),
				}
				if len(msg.Attachments) > 0 {
					data["attachments"] = serializeAttachments(msg.Attachments)
				}
				return app.Out.Print(data, fmt.Sprintf("Sent message %s to #%s", msg.ID, ch.Name()))
			})
		},
	}
	sendCmd.Flags().StringVar(&embedTitle, "embed-title", "", "Embed title")
	sendCmd.Flags().StringVar(&embedDesc, "embed-desc", "", "Embed description")
	sendCmd.Flags().StringVar(&embedColor, "embed-color", "", "Embed hex color (e.g. 5865F2)")
	sendCmd.Flags().StringVar(&embedFooter, "embed-footer", "", "Embed footer text")
	sendCmd.Flags().StringVar(&embedImage, "embed-image", "", "Embed image URL")
	sendCmd.Flags().StringVar(&embedThumb, "embed-thumbnail", "", "Embed thumbnail URL")
	sendCmd.Flags().StringVar(&embedAuthor, "embed-author", "", "Embed author name")
	sendCmd.Flags().StringArrayVar(&embedFields, "embed-field", nil, "Embed field Name::Value[::Inline] (repeatable)")
	sendCmd.Flags().StringArrayVar(&sendFiles, "file", nil, "File path to attach (repeatable)")

	// message list
	var listLimit int
	var listBefore, listAfter string
	listCmd := &cobra.Command{
		Use:   "list <channel>",
		Short: "List recent messages in a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				beforeID, err := parseDateSnowflake(listBefore)
				if err != nil {
					return err
				}
				afterID, err := parseDateSnowflake(listAfter)
				if err != nil {
					return err
				}
				messages, err := fetchChannelMessages(client, ch.ID(), listLimit, beforeID, afterID, nil)
				if err != nil {
					return err
				}
				data, plain := formatMessageList(messages)
				return app.Out.Print(data, plain)
			})
		},
	}
	listCmd.Flags().IntVar(&listLimit, "limit", 10, "Number of messages to fetch")
	listCmd.Flags().StringVar(&listBefore, "before", "", "Before date (YYYY-MM-DD or ISO 8601)")
	listCmd.Flags().StringVar(&listAfter, "after", "", "After date (YYYY-MM-DD or ISO 8601)")

	// message history
	var histDays, histHours, histLimit int
	historyCmd := &cobra.Command{
		Use:   "history <channel>",
		Short: "Fetch deep message history from a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				var afterID snowflake.ID
				if histDays > 0 {
					afterID = snowflake.New(time.Now().UTC().Add(-time.Duration(histDays) * 24 * time.Hour))
				} else if histHours > 0 {
					afterID = snowflake.New(time.Now().UTC().Add(-time.Duration(histHours) * time.Hour))
				}
				messages, err := fetchChannelMessages(client, ch.ID(), histLimit, 0, afterID, func(count int) {
					if count%100 == 0 {
						app.Out.Note("Fetched %d messages...", count)
					}
				})
				if err != nil {
					return err
				}
				app.Out.Note("Total: %d messages", len(messages))
				data, plain := formatMessageList(messages)
				return app.Out.Print(data, plain)
			})
		},
	}
	historyCmd.Flags().IntVar(&histDays, "days", 0, "Fetch messages from the last N days")
	historyCmd.Flags().IntVar(&histHours, "hours", 0, "Fetch messages from the last N hours")
	historyCmd.Flags().IntVar(&histLimit, "limit", 0, "Maximum messages to fetch (0 = unlimited)")

	// message get
	getCmd := &cobra.Command{
		Use:   "get <channel> <message_id>",
		Short: "Fetch a single message by ID",
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
				atts := make([]map[string]any, 0, len(msg.Attachments))
				attNames := make([]string, 0, len(msg.Attachments))
				for _, a := range msg.Attachments {
					atts = append(atts, map[string]any{"filename": a.Filename, "url": a.URL})
					attNames = append(attNames, a.Filename)
				}
				embeds := make([]map[string]any, 0, len(msg.Embeds))
				for _, e := range msg.Embeds {
					embeds = append(embeds, map[string]any{"title": e.Title, "description": e.Description})
				}
				var replyTo any
				if msg.MessageReference != nil && msg.MessageReference.MessageID != nil {
					replyTo = msg.MessageReference.MessageID.String()
				}
				author := voxdiscord.FormatUserTag(msg.Author)
				data := map[string]any{
					"id":          msg.ID.String(),
					"author":      author,
					"author_id":   msg.Author.ID.String(),
					"content":     msg.Content,
					"timestamp":   msg.CreatedAt.UTC().Format(time.RFC3339),
					"attachments": atts,
					"embeds":      embeds,
					"reply_to":    replyTo,
					"jump_url":    msg.JumpURL(),
				}
				lines := []string{
					fmt.Sprintf("From: %s (ID: %s)", author, msg.Author.ID),
					fmt.Sprintf("At: %s", msg.CreatedAt.UTC().Format("2006-01-02 15:04:05")),
					fmt.Sprintf("Content: %s", msg.Content),
				}
				if len(attNames) > 0 {
					lines = append(lines, "Attachments: "+strings.Join(attNames, ", "))
				}
				if replyTo != nil {
					lines = append(lines, fmt.Sprintf("Reply to: %v", replyTo))
				}
				return app.Out.Print(data, strings.Join(lines, "\n"))
			})
		},
	}

	// message edit
	editCmd := &cobra.Command{
		Use:   "edit <channel> <message_id> <new_text>",
		Short: "Edit a message authored by the bot",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid message ID: %s", args[1]))
			}
			newText := args[2]
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				msg, err := client.UpdateMessage(ch.ID(), msgID, disgodiscord.MessageUpdate{Content: &newText})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(map[string]any{"id": msg.ID.String(), "content": newText}, fmt.Sprintf("Edited message %s", msg.ID))
			})
		},
	}

	// message delete
	deleteCmd := &cobra.Command{
		Use:   "delete <channel> <message_id>",
		Short: "Delete a message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, ok := voxdiscord.ParseSnowflake(args[1])
			if !ok {
				return output.UsageError(fmt.Sprintf("invalid message ID: %s", args[1]))
			}
			if err := app.ConfirmDestructive("message delete", fmt.Sprintf("message %s in %s", args[1], args[0])); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				if err := client.DeleteMessage(ch.ID(), msgID); err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("message delete", map[string]any{"channel": args[0], "message_id": args[1]}, "ok", "")
				return app.Out.Print(map[string]any{"id": msgID.String(), "deleted": true}, fmt.Sprintf("Deleted message %s", msgID))
			})
		},
	}

	// message bulk-delete
	bulkDeleteCmd := &cobra.Command{
		Use:   "bulk-delete <channel> <message_id>...",
		Short: "Delete multiple messages at once",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			channelRef := args[0]
			rawIDs := args[1:]
			ids := make([]snowflake.ID, 0, len(rawIDs))
			for _, raw := range rawIDs {
				id, ok := voxdiscord.ParseSnowflake(raw)
				if !ok {
					return output.UsageError(fmt.Sprintf("invalid message ID: %s", raw))
				}
				ids = append(ids, id)
			}
			if err := app.ConfirmDestructive("message bulk-delete", fmt.Sprintf("%d messages in %s", len(ids), channelRef)); err != nil {
				return err
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, channelRef)
				if err != nil {
					return err
				}
				if len(ids) == 1 {
					err = client.DeleteMessage(ch.ID(), ids[0])
				} else {
					err = client.BulkDeleteMessages(ch.ID(), ids)
				}
				if err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("message bulk-delete", map[string]any{"channel": channelRef, "count": len(ids)}, "ok", "")
				return app.Out.Print(map[string]any{"deleted": len(ids)}, fmt.Sprintf("Deleted %d messages", len(ids)))
			})
		},
	}

	// message reply
	var replyFiles []string
	replyCmd := &cobra.Command{
		Use:   "reply <channel> <message_id> <text>",
		Short: "Reply to a specific message",
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
				files, cleanup, err := voxdiscord.OpenFiles(replyFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				msg, err := client.CreateMessage(ch.ID(), disgodiscord.MessageCreate{
					Content:          args[2],
					Files:            files,
					MessageReference: &disgodiscord.MessageReference{MessageID: &msgID},
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := map[string]any{
					"id":       msg.ID.String(),
					"channel":  ch.Name(),
					"content":  msg.Content,
					"reply_to": args[1],
					"jump_url": msg.JumpURL(),
				}
				if len(msg.Attachments) > 0 {
					data["attachments"] = serializeAttachments(msg.Attachments)
				}
				return app.Out.Print(data, fmt.Sprintf("Replied to %s in #%s", args[1], ch.Name()))
			})
		},
	}
	replyCmd.Flags().StringArrayVar(&replyFiles, "file", nil, "File path to attach (repeatable)")

	// message search (single-channel client-side scan)
	var searchLimit, searchScan int
	var searchAuthor, searchBefore, searchAfter string
	searchCmd := &cobra.Command{
		Use:   "search <channel> <query>",
		Short: "Search messages in a single channel (client-side scan)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.ToLower(args[1])
			authorNeedle := strings.ToLower(searchAuthor)
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				beforeID, err := parseDateSnowflake(searchBefore)
				if err != nil {
					return err
				}
				afterID, err := parseDateSnowflake(searchAfter)
				if err != nil {
					return err
				}
				scanned, err := fetchChannelMessages(client, ch.ID(), searchScan, beforeID, afterID, nil)
				if err != nil {
					return err
				}
				var matched []disgodiscord.Message
				for _, m := range scanned {
					if authorNeedle != "" && !strings.Contains(strings.ToLower(voxdiscord.FormatUserTag(m.Author)), authorNeedle) {
						continue
					}
					haystack := strings.ToLower(m.Content + " " + extractEmbedText(m.Embeds))
					if strings.Contains(haystack, query) {
						matched = append(matched, m)
						if searchLimit > 0 && len(matched) >= searchLimit {
							break
						}
					}
				}
				data, _ := formatMessageList(matched)
				if len(matched) == 0 {
					return app.Out.Print(data, fmt.Sprintf("No messages matching %q found.", args[1]))
				}
				lines := []string{fmt.Sprintf("Found %d match(es):", len(matched))}
				for _, m := range matched {
					lines = append(lines, fmt.Sprintf("[%s] (msg:%s) %s: %s",
						m.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
						m.ID, voxdiscord.FormatUserTag(m.Author), m.Content))
				}
				return app.Out.Print(data, strings.Join(lines, "\n"))
			})
		},
	}
	searchCmd.Flags().IntVar(&searchLimit, "limit", 0, "Max matching messages to return (0 = all matches)")
	searchCmd.Flags().IntVar(&searchScan, "scan", 500, "Number of channel messages to scan")
	searchCmd.Flags().StringVar(&searchAuthor, "author", "", "Filter by author name substring")
	searchCmd.Flags().StringVar(&searchBefore, "before", "", "Before date (YYYY-MM-DD or ISO 8601)")
	searchCmd.Flags().StringVar(&searchAfter, "after", "", "After date (YYYY-MM-DD or ISO 8601)")

	// message search-server (Discord's server-wide indexed search endpoint)
	var (
		ssAuthors, ssAuthorTypes, ssChannels, ssMentions, ssHas, ssExtensions []string
		ssPinned, ssNoPinned                                                  bool
		ssSortBy, ssSortOrder                                                 string
		ssLimit, ssOffset                                                     int
	)
	searchServerCmd := &cobra.Command{
		Use:   "search-server <server> [query]",
		Short: "Search messages across an entire server using Discord's search index",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			serverRef := args[0]
			query := ""
			if len(args) == 2 {
				query = args[1]
			}
			if len(query) > 1024 {
				return output.UsageError("query cannot exceed 1024 characters")
			}
			if ssLimit < 1 || ssLimit > 25 {
				return output.UsageError("--limit must be between 1 and 25")
			}
			if ssOffset < 0 || ssOffset > 9975 {
				return output.UsageError("--offset must be between 0 and 9975")
			}
			if ssPinned && ssNoPinned {
				return output.UsageError("cannot pass both --pinned and --no-pinned")
			}
			hasAnyFilter := query != "" || len(ssAuthors) > 0 || len(ssAuthorTypes) > 0 ||
				len(ssChannels) > 0 || len(ssMentions) > 0 || len(ssHas) > 0 ||
				len(ssExtensions) > 0 || ssPinned || ssNoPinned
			if !hasAnyFilter {
				return output.UsageError("provide a query or at least one filter (--author, --channel, --mentions, --has, --pinned, ...)")
			}

			return app.WithRest(cmd, func(client rest.Rest) error {
				guild, err := voxdiscord.ResolveGuild(client, serverRef)
				if err != nil {
					return err
				}
				req := disgodiscord.GuildMessagesSearch{
					Content: query,
					Limit:   ssLimit,
					Offset:  ssOffset,
				}
				if ssPinned {
					t := true
					req.Pinned = &t
				} else if ssNoPinned {
					f := false
					req.Pinned = &f
				}
				for _, a := range ssAuthors {
					if id, ok := voxdiscord.ParseSnowflake(a); ok {
						req.AuthorIDs = append(req.AuthorIDs, id)
					} else {
						member, err := voxdiscord.ResolveMember(client, guild.ID, a)
						if err != nil {
							return err
						}
						req.AuthorIDs = append(req.AuthorIDs, member.User.ID)
					}
				}
				for _, m := range ssMentions {
					if id, ok := voxdiscord.ParseSnowflake(m); ok {
						req.Mentions = append(req.Mentions, id)
					} else {
						member, err := voxdiscord.ResolveMember(client, guild.ID, m)
						if err != nil {
							return err
						}
						req.Mentions = append(req.Mentions, member.User.ID)
					}
				}
				for _, chRef := range ssChannels {
					ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, chRef)
					if err != nil {
						return err
					}
					req.ChannelIDs = append(req.ChannelIDs, ch.ID())
				}
				for _, t := range ssAuthorTypes {
					req.AuthorTypes = append(req.AuthorTypes, disgodiscord.MessageSearchAuthorType(t))
				}
				for _, h := range ssHas {
					req.Has = append(req.Has, disgodiscord.MessageSearchHasType(h))
				}
				for _, ext := range ssExtensions {
					req.AttachmentExtensions = append(req.AttachmentExtensions, strings.TrimPrefix(ext, "."))
				}
				if ssSortBy != "" {
					req.SortBy = disgodiscord.MessageSearchSortBy(ssSortBy)
				}
				if ssSortOrder != "" {
					req.SortOrder = disgodiscord.MessageSearchSortOrder(ssSortOrder)
				}

				res, err := client.SearchGuildMessages(cmd.Context(), guild.ID, req)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				if res == nil || res.RetryAfter > 0 || (res.DoingDeepHistoricalIndex && len(res.Messages) == 0) {
					return output.NewError("search_indexing", fmt.Sprintf(
						"Discord has not finished indexing %q for search. Try again shortly, or scan one channel with `vox message search <channel> <query>`.",
						guild.Name,
					))
				}

				results := make([]map[string]any, 0, len(res.Messages))
				plainLines := make([]string, 0, len(res.Messages)*2)
				for _, hit := range res.Messages {
					authorName := hit.Author.Username
					if hit.Author.GlobalName != nil && *hit.Author.GlobalName != "" {
						authorName = *hit.Author.GlobalName
					}
					jumpURL := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guild.ID, hit.ChannelID, hit.ID)
					results = append(results, map[string]any{
						"id":          hit.ID.String(),
						"channel_id":  hit.ChannelID.String(),
						"author":      authorName,
						"author_id":   hit.Author.ID.String(),
						"is_bot":      hit.Author.Bot,
						"content":     hit.Content,
						"timestamp":   hit.CreatedAt.UTC().Format(time.RFC3339),
						"pinned":      hit.Pinned,
						"attachments": serializeAttachments(hit.Attachments),
						"jump_url":    jumpURL,
					})
					plainLines = append(plainLines,
						fmt.Sprintf("[%s] %s: %s", hit.CreatedAt.UTC().Format("2006-01-02 15:04:05"), authorName, hit.Content),
						"    "+jumpURL,
					)
				}
				payload := map[string]any{
					"total_results": res.TotalResults,
					"returned":      len(results),
					"offset":        ssOffset,
					"results":       results,
				}
				plain := "No matching messages."
				if len(results) > 0 {
					plain = strings.Join(plainLines, "\n") + fmt.Sprintf("\n\n%d of %d result(s)", len(results), res.TotalResults)
				}
				return app.Out.Print(payload, plain)
			})
		},
	}
	searchServerCmd.Flags().StringArrayVar(&ssAuthors, "author", nil, "Filter by author name or ID (repeatable)")
	searchServerCmd.Flags().StringArrayVar(&ssAuthorTypes, "author-type", nil, "Filter by author type: user, bot, webhook (repeatable)")
	searchServerCmd.Flags().StringArrayVar(&ssChannels, "channel", nil, "Filter by channel name or ID (repeatable)")
	searchServerCmd.Flags().StringArrayVar(&ssMentions, "mentions", nil, "Filter by mentioned user name or ID (repeatable)")
	searchServerCmd.Flags().StringArrayVar(&ssHas, "has", nil, "Filter by content type: link, embed, file, image, video, sound, sticker, poll")
	searchServerCmd.Flags().StringArrayVar(&ssExtensions, "extension", nil, "Filter by attachment extension (repeatable)")
	searchServerCmd.Flags().BoolVar(&ssPinned, "pinned", false, "Only pinned messages")
	searchServerCmd.Flags().BoolVar(&ssNoPinned, "no-pinned", false, "Only unpinned messages")
	searchServerCmd.Flags().StringVar(&ssSortBy, "sort-by", "timestamp", "Sort by: timestamp or relevance")
	searchServerCmd.Flags().StringVar(&ssSortOrder, "sort-order", "desc", "Sort order: desc or asc")
	searchServerCmd.Flags().IntVar(&ssLimit, "limit", 25, "Results per page (1-25)")
	searchServerCmd.Flags().IntVar(&ssOffset, "offset", 0, "Result offset (0-9975)")

	// message pin / unpin / pins
	var pinReason, unpinReason string
	pinCmd := &cobra.Command{
		Use:   "pin <channel> <message_id>",
		Short: "Pin a message in a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagePin(app, cmd, args[0], args[1], pinReason, true)
		},
	}
	pinCmd.Flags().StringVar(&pinReason, "reason", "", "Audit log reason")

	unpinCmd := &cobra.Command{
		Use:   "unpin <channel> <message_id>",
		Short: "Unpin a message in a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagePin(app, cmd, args[0], args[1], unpinReason, false)
		},
	}
	unpinCmd.Flags().StringVar(&unpinReason, "reason", "", "Audit log reason")

	var pinsLimit int
	pinsCmd := &cobra.Command{
		Use:   "pins <channel>",
		Short: "List pinned messages in a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				pins, err := client.GetChannelPins(ch.ID(), time.Time{}, pinsLimit)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(pins.Items))
				lines := make([]string, 0, len(pins.Items))
				for _, item := range pins.Items {
					m := item.Message
					author := voxdiscord.FormatUserTag(m.Author)
					data = append(data, map[string]any{
						"id":        m.ID.String(),
						"author":    author,
						"content":   m.Content,
						"timestamp": m.CreatedAt.UTC().Format(time.RFC3339),
						"jump_url":  m.JumpURL(),
					})
					lines = append(lines, fmt.Sprintf("[%s] %s: %s (ID: %s)",
						m.CreatedAt.UTC().Format("2006-01-02 15:04:05"), author, m.Content, m.ID))
				}
				plain := "No pinned messages."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}
	pinsCmd.Flags().IntVar(&pinsLimit, "limit", 50, "Maximum pinned messages to list")

	group.AddCommand(
		sendCmd, listCmd, historyCmd, getCmd, editCmd, deleteCmd,
		bulkDeleteCmd, replyCmd, searchCmd, searchServerCmd, pinCmd, unpinCmd, pinsCmd,
	)
	return group
}

func runMessagePin(app *App, cmd *cobra.Command, channelRef, messageIDRaw, reason string, pin bool) error {
	msgID, ok := voxdiscord.ParseSnowflake(messageIDRaw)
	if !ok {
		return output.UsageError(fmt.Sprintf("invalid message ID: %s", messageIDRaw))
	}
	return app.WithRest(cmd, func(client rest.Rest) error {
		ch, err := voxdiscord.ResolveChannel(client, channelRef)
		if err != nil {
			return err
		}
		var opts []rest.RequestOpt
		if reason != "" {
			opts = append(opts, rest.WithReason(reason))
		}
		if pin {
			err = client.PinMessage(ch.ID(), msgID, opts...)
		} else {
			err = client.UnpinMessage(ch.ID(), msgID, opts...)
		}
		if err != nil {
			return voxdiscord.MapError(err)
		}
		auditName := "message unpin"
		verb := "Unpinned"
		if pin {
			auditName = "message pin"
			verb = "Pinned"
		}
		_ = app.Security.Audit.Record(auditName, map[string]any{"channel": channelRef, "message_id": messageIDRaw}, "ok", "")
		return app.Out.Print(map[string]any{"id": msgID.String(), "pinned": pin}, fmt.Sprintf("%s message %s", verb, msgID))
	})
}

func newDMCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "dm",
		Short:   "Send and read direct messages",
		GroupID: "messaging",
	}

	var dmFiles []string
	sendCmd := &cobra.Command{
		Use:   "send <user> <text>",
		Short: "Send a direct message to a user",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				user, err := voxdiscord.ResolveUser(client, args[0])
				if err != nil {
					return err
				}
				dm, err := client.CreateDMChannel(user.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				files, cleanup, err := voxdiscord.OpenFiles(dmFiles)
				if err != nil {
					return err
				}
				defer cleanup()
				msg, err := client.CreateMessage(dm.ID(), disgodiscord.MessageCreate{
					Content: args[1],
					Files:   files,
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(*user)
				data := map[string]any{
					"id":      msg.ID.String(),
					"to":      tag,
					"to_id":   user.ID.String(),
					"content": msg.Content,
				}
				if len(msg.Attachments) > 0 {
					data["attachments"] = serializeAttachments(msg.Attachments)
				}
				preview := args[1]
				if len(preview) > 50 {
					preview = preview[:50]
				}
				return app.Out.Print(data, fmt.Sprintf("Sent DM to %s (ID: %s): %s", tag, user.ID, preview))
			})
		},
	}
	sendCmd.Flags().StringArrayVar(&dmFiles, "file", nil, "File path to attach (repeatable)")

	var dmLimit int
	listCmd := &cobra.Command{
		Use:   "list <user>",
		Short: "List recent direct messages with a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				user, err := voxdiscord.ResolveUser(client, args[0])
				if err != nil {
					return err
				}
				dm, err := client.CreateDMChannel(user.ID)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				messages, err := fetchChannelMessages(client, dm.ID(), dmLimit, 0, 0, nil)
				if err != nil {
					return err
				}
				data := make([]map[string]any, 0, len(messages))
				lines := make([]string, 0, len(messages))
				for _, m := range messages {
					author := voxdiscord.FormatUserTag(m.Author)
					data = append(data, map[string]any{
						"id":        m.ID.String(),
						"author":    author,
						"author_id": m.Author.ID.String(),
						"content":   m.Content,
						"timestamp": m.CreatedAt.UTC().Format(time.RFC3339),
					})
					lines = append(lines, fmt.Sprintf("[%s] (msg:%s) %s: %s",
						m.CreatedAt.UTC().Format("2006-01-02 15:04:05"), m.ID, author, m.Content))
				}
				plain := "No DMs found."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}
	listCmd.Flags().IntVar(&dmLimit, "limit", 10, "Number of messages to fetch")

	group.AddCommand(sendCmd, listCmd)
	return group
}

func newReactionCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "reaction",
		Short:   "Add, remove, and inspect emoji reactions",
		GroupID: "messaging",
	}

	addCmd := &cobra.Command{
		Use:   "add <channel> <message_id> <emoji>",
		Short: "Add a reaction to a message",
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
				if err := client.AddReaction(ch.ID(), msgID, args[2]); err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(map[string]any{"message_id": args[1], "emoji": args[2]}, fmt.Sprintf("Reacted %s to message %s", args[2], args[1]))
			})
		},
	}

	removeCmd := &cobra.Command{
		Use:   "remove <channel> <message_id> <emoji>",
		Short: "Remove the bot's reaction from a message",
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
				if err := client.RemoveOwnReaction(ch.ID(), msgID, args[2]); err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(map[string]any{"message_id": args[1], "emoji": args[2]}, fmt.Sprintf("Removed %s from message %s", args[2], args[1]))
			})
		},
	}

	listCmd := &cobra.Command{
		Use:   "list <channel> <message_id>",
		Short: "List reactions on a message",
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
				data := make([]map[string]any, 0, len(msg.Reactions))
				lines := make([]string, 0, len(msg.Reactions))
				for _, r := range msg.Reactions {
					emoji := r.Emoji.Reaction()
					data = append(data, map[string]any{"emoji": emoji, "count": r.Count})
					lines = append(lines, fmt.Sprintf("%s x%d", emoji, r.Count))
				}
				plain := "No reactions."
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}

	var usersLimit int
	usersCmd := &cobra.Command{
		Use:   "users <channel> <message_id> <emoji>",
		Short: "List users who reacted with a specific emoji",
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
				users, err := client.GetReactions(ch.ID(), msgID, args[2], disgodiscord.MessageReactionTypeNormal, 0, usersLimit)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				data := make([]map[string]any, 0, len(users))
				lines := make([]string, 0, len(users))
				for _, u := range users {
					name := voxdiscord.FormatUserTag(u)
					data = append(data, map[string]any{"id": u.ID.String(), "name": name, "bot": u.Bot})
					line := fmt.Sprintf("%s (ID: %s)", name, u.ID)
					if u.Bot {
						line += " [bot]"
					}
					lines = append(lines, line)
				}
				plain := fmt.Sprintf("No %s reactions found.", args[2])
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(data, plain)
			})
		},
	}
	usersCmd.Flags().IntVar(&usersLimit, "limit", 100, "Maximum users to list")

	group.AddCommand(addCmd, removeCmd, listCmd, usersCmd)
	return group
}

func newTypingCommand(app *App) *cobra.Command {
	var duration int
	cmd := &cobra.Command{
		Use:     "typing <channel>",
		Short:   "Show the typing indicator in a channel",
		GroupID: "messaging",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if duration < 0 {
				return output.UsageError("--duration must be >= 0")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, args[0])
				if err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(duration)*time.Second)
				defer cancel()

				if err := client.SendTyping(ch.ID()); err != nil {
					return voxdiscord.MapError(err)
				}
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
			loop:
				for {
					select {
					case <-ctx.Done():
						break loop
					case <-ticker.C:
						_ = client.SendTyping(ch.ID())
					}
				}
				return app.Out.Print(
					map[string]any{"channel": ch.Name(), "channel_id": ch.ID().String(), "duration": duration},
					fmt.Sprintf("Typed in #%s for %ds", ch.Name(), duration),
				)
			})
		},
	}
	cmd.Flags().IntVar(&duration, "duration", 5, "Seconds to show typing indicator")
	return cmd
}

func serializeAttachments(atts []disgodiscord.Attachment) []map[string]any {
	out := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, map[string]any{
			"filename": a.Filename,
			"url":      a.URL,
			"size":     a.Size,
		})
	}
	return out
}

func parseDateSnowflake(raw string) (snowflake.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	if id, ok := voxdiscord.ParseSnowflake(trimmed); ok {
		return id, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return snowflake.New(parsed.UTC()), nil
		}
	}
	return 0, output.UsageError(fmt.Sprintf("invalid date %q (expected YYYY-MM-DD or ISO 8601)", raw))
}

func fetchChannelMessages(
	client rest.Rest,
	channelID snowflake.ID,
	limit int,
	before, after snowflake.ID,
	onProgress func(int),
) ([]disgodiscord.Message, error) {
	var all []disgodiscord.Message
	cursorBefore := before
	for {
		batchSize := 100
		if limit > 0 {
			remaining := limit - len(all)
			if remaining <= 0 {
				break
			}
			if remaining < batchSize {
				batchSize = remaining
			}
		}
		batch, err := client.GetMessages(channelID, 0, cursorBefore, after, batchSize)
		if err != nil {
			return nil, voxdiscord.MapError(err)
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if onProgress != nil {
			onProgress(len(all))
		}
		if len(batch) < batchSize {
			break
		}
		cursorBefore = batch[len(batch)-1].ID
		after = 0
	}
	// Preserve chronological readability if paging upward via after.
	if before == 0 && after != 0 {
		slices.Reverse(all)
	}
	return all, nil
}

func formatMessageList(messages []disgodiscord.Message) ([]map[string]any, string) {
	data := make([]map[string]any, 0, len(messages))
	lines := make([]string, 0, len(messages))
	for _, m := range messages {
		author := voxdiscord.FormatUserTag(m.Author)
		atts := serializeAttachments(m.Attachments)
		embeds := make([]map[string]any, 0, len(m.Embeds))
		for _, e := range m.Embeds {
			fields := make([]map[string]any, 0, len(e.Fields))
			for _, f := range e.Fields {
				inline := f.Inline != nil && *f.Inline
				fields = append(fields, map[string]any{"name": f.Name, "value": f.Value, "inline": inline})
			}
			var footerText, authorText any
			if e.Footer != nil {
				footerText = e.Footer.Text
			}
			if e.Author != nil {
				authorText = e.Author.Name
			}
			embeds = append(embeds, map[string]any{
				"title":       e.Title,
				"description": e.Description,
				"fields":      fields,
				"footer":      footerText,
				"author":      authorText,
			})
		}
		data = append(data, map[string]any{
			"id":          m.ID.String(),
			"author":      author,
			"content":     m.Content,
			"timestamp":   m.CreatedAt.UTC().Format(time.RFC3339),
			"attachments": atts,
			"embeds":      embeds,
		})
		line := fmt.Sprintf("[%s] %s: %s", m.CreatedAt.UTC().Format("2006-01-02 15:04:05"), author, m.Content)
		if len(m.Attachments) > 0 {
			names := make([]string, len(m.Attachments))
			for i, a := range m.Attachments {
				names[i] = a.Filename
			}
			line += " [" + strings.Join(names, ", ") + "]"
		}
		if summary := extractEmbedText(m.Embeds); summary != "" {
			line += " {" + summary + "}"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return data, "No messages found."
	}
	return data, strings.Join(lines, "\n")
}

func extractEmbedText(embeds []disgodiscord.Embed) string {
	var parts []string
	for _, e := range embeds {
		if e.Author != nil && e.Author.Name != "" {
			parts = append(parts, e.Author.Name)
		}
		if e.Title != "" {
			parts = append(parts, "["+e.Title+"]")
		}
		if e.Description != "" {
			parts = append(parts, e.Description)
		}
		for _, f := range e.Fields {
			parts = append(parts, f.Name+": "+f.Value)
		}
		if e.Footer != nil && e.Footer.Text != "" {
			parts = append(parts, "("+e.Footer.Text+")")
		}
	}
	return strings.Join(parts, " | ")
}
