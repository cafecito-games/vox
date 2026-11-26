package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/interact"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/scheduler"
	"github.com/cafecito-games/vox/internal/serve"
	"github.com/cafecito-games/vox/internal/voice"
)

func newListenCommand(app *App) *cobra.Command {
	var serverFilter, channelFilter, eventsFlag string
	var includeBots bool

	cmd := &cobra.Command{
		Use:     "listen",
		Short:   "Stream live Discord events as plain text or compact JSONL (--json)",
		GroupID: "realtime",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := app.ResolveToken(cmd)
			if err != nil {
				return err
			}
			eventFilter, err := voxdiscord.ParseEventFilter(eventsFlag)
			if err != nil {
				return err
			}
			features := voxdiscord.GatewayFeaturesForEvents(eventFilter)
			enabled := func(cat string) bool {
				return len(eventFilter) == 0 || eventFilter[cat]
			}

			var client *bot.Client
			var botID snowflake.ID
			matchesFilter := func(guildID *snowflake.ID, channelID snowflake.ID) bool {
				if guildID == nil {
					return true
				}
				if serverFilter != "" {
					g, ok := client.Caches.Guild(*guildID)
					if guildID.String() != serverFilter && (!ok || !strings.EqualFold(g.Name, serverFilter)) {
						return false
					}
				}
				if channelFilter != "" {
					want := strings.TrimPrefix(channelFilter, "#")
					ch, ok := client.Caches.Channel(channelID)
					if channelID.String() != want && (!ok || !strings.EqualFold(ch.Name(), want)) {
						return false
					}
				}
				return true
			}

			client, err = voxdiscord.NewGatewayClient(
				token,
				features,
				app.Logger,
				bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagChannels, cache.FlagMessages)),
				bot.WithEventListenerFunc(func(ev *events.Ready) {
					botID = ev.User.ID
					app.Out.Note("Listening as %s... (Ctrl-C to stop)", ev.User.Username)
				}),
				bot.WithEventListenerFunc(func(ev *events.MessageCreate) {
					if !enabled("messages") || (!includeBots && ev.Message.Author.Bot) {
						return
					}
					if !matchesFilter(ev.GuildID, ev.ChannelID) {
						return
					}
					chName := "DM"
					serverName := "DM"
					var serverID any
					if ev.GuildID != nil {
						serverID = ev.GuildID.String()
						if g, ok := client.Caches.Guild(*ev.GuildID); ok {
							serverName = g.Name
						}
						if ch, ok := client.Caches.Channel(ev.ChannelID); ok {
							chName = ch.Name()
						}
					}
					mentionsBot := false
					for _, u := range ev.Message.Mentions {
						if u.ID == botID {
							mentionsBot = true
							break
						}
					}
					author := voxdiscord.FormatUserTag(ev.Message.Author)
					payload := map[string]any{
						"event":        "message",
						"server":       serverName,
						"server_id":    serverID,
						"channel":      chName,
						"channel_id":   ev.ChannelID.String(),
						"author":       author,
						"author_id":    ev.Message.Author.ID.String(),
						"content":      ev.Message.Content,
						"timestamp":    ev.Message.CreatedAt.UTC().Format(time.RFC3339),
						"message_id":   ev.Message.ID.String(),
						"mentions_bot": mentionsBot,
					}
					plain := fmt.Sprintf("[%s] (msg:%s) %s (uid:%s) in #%s (ch:%s): %s",
						ev.Message.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
						ev.Message.ID, author, ev.Message.Author.ID, chName, ev.ChannelID, ev.Message.Content)
					_ = app.Out.PrintStream(payload, plain)
				}),
				bot.WithEventListenerFunc(func(ev *events.MessageUpdate) {
					if !enabled("edits") || (!includeBots && ev.Message.Author.Bot) || !matchesFilter(ev.GuildID, ev.ChannelID) {
						return
					}
					author := voxdiscord.FormatUserTag(ev.Message.Author)
					payload := map[string]any{
						"event":       "message_edit",
						"channel_id":  ev.ChannelID.String(),
						"author":      author,
						"author_id":   ev.Message.Author.ID.String(),
						"message_id":  ev.MessageID.String(),
						"old_content": ev.OldMessage.Content,
						"new_content": ev.Message.Content,
					}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("EDIT (msg:%s) %s: %s -> %s", ev.MessageID, author, ev.OldMessage.Content, ev.Message.Content))
				}),
				bot.WithEventListenerFunc(func(ev *events.MessageDelete) {
					if !enabled("deletes") || !matchesFilter(ev.GuildID, ev.ChannelID) {
						return
					}
					payload := map[string]any{
						"event":      "message_delete",
						"channel_id": ev.ChannelID.String(),
						"message_id": ev.MessageID.String(),
						"content":    ev.Message.Content,
					}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("DELETE (msg:%s) in %s: %s", ev.MessageID, ev.ChannelID, ev.Message.Content))
				}),
				bot.WithEventListenerFunc(func(ev *events.MessageReactionAdd) {
					if !enabled("reactions") || !matchesFilter(ev.GuildID, ev.ChannelID) {
						return
					}
					payload := map[string]any{
						"event":      "reaction_add",
						"channel_id": ev.ChannelID.String(),
						"message_id": ev.MessageID.String(),
						"emoji":      ev.Emoji.Reaction(),
						"user_id":    ev.UserID.String(),
					}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("uid:%s reacted %s on msg:%s", ev.UserID, ev.Emoji.Reaction(), ev.MessageID))
				}),
				bot.WithEventListenerFunc(func(ev *events.MessageReactionRemove) {
					if !enabled("reactions") || !matchesFilter(ev.GuildID, ev.ChannelID) {
						return
					}
					payload := map[string]any{
						"event":      "reaction_remove",
						"channel_id": ev.ChannelID.String(),
						"message_id": ev.MessageID.String(),
						"emoji":      ev.Emoji.Reaction(),
						"user_id":    ev.UserID.String(),
					}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("uid:%s removed %s from msg:%s", ev.UserID, ev.Emoji.Reaction(), ev.MessageID))
				}),
				bot.WithEventListenerFunc(func(ev *events.GuildMemberJoin) {
					if !enabled("members") {
						return
					}
					tag := voxdiscord.FormatUserTag(ev.Member.User)
					payload := map[string]any{"event": "member_join", "server_id": ev.GuildID.String(), "member": tag, "member_id": ev.Member.User.ID.String()}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("%s (uid:%s) joined %s", tag, ev.Member.User.ID, ev.GuildID))
				}),
				bot.WithEventListenerFunc(func(ev *events.GuildMemberLeave) {
					if !enabled("members") {
						return
					}
					tag := voxdiscord.FormatUserTag(ev.User)
					payload := map[string]any{"event": "member_remove", "server_id": ev.GuildID.String(), "member": tag, "member_id": ev.User.ID.String()}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("%s (uid:%s) left %s", tag, ev.User.ID, ev.GuildID))
				}),
				bot.WithEventListenerFunc(func(ev *events.GuildVoiceStateUpdate) {
					if !enabled("voice") || (ev.OldVoiceState.ChannelID == nil && ev.VoiceState.ChannelID == nil) {
						return
					}
					action := "updated"
					switch {
					case ev.OldVoiceState.ChannelID == nil && ev.VoiceState.ChannelID != nil:
						action = "joined"
					case ev.OldVoiceState.ChannelID != nil && ev.VoiceState.ChannelID == nil:
						action = "left"
					case *ev.OldVoiceState.ChannelID != *ev.VoiceState.ChannelID:
						action = "moved"
					}
					payload := map[string]any{
						"event":     "voice_state",
						"action":    action,
						"server_id": ev.VoiceState.GuildID.String(),
						"member_id": ev.VoiceState.UserID.String(),
					}
					_ = app.Out.PrintStream(payload, fmt.Sprintf("uid:%s voice %s", ev.VoiceState.UserID, action))
				}),
			)
			if err != nil {
				return err
			}
			if err := client.OpenGateway(cmd.Context()); err != nil {
				return voxdiscord.MapError(err)
			}
			defer func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				client.Close(closeCtx)
			}()

			<-cmd.Context().Done()
			app.Out.Note("\nStopped listening.")
			return nil
		},
	}

	cmd.Flags().StringVar(&serverFilter, "server", "", "Filter events by server name or ID")
	cmd.Flags().StringVar(&channelFilter, "channel", "", "Filter events by channel name or ID")
	cmd.Flags().StringVar(&eventsFlag, "events", "", "Comma-separated event categories: messages,reactions,members,edits,deletes,voice")
	cmd.Flags().BoolVar(&includeBots, "include-bots", false, "Include bot messages and edits (ignored by default)")
	return cmd
}

func newServeCommand(app *App) *cobra.Command {
	var (
		serverFilter, channelFilter, eventsFlag string
		noIncludeSelf                           bool
		slashFile, status, activity, actText    string
	)
	cmd := &cobra.Command{
		Use:     "serve",
		Short:   "Run a persistent bot daemon with bidirectional stdin/stdout JSONL (73 actions)",
		GroupID: "realtime",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := app.ResolveToken(cmd)
			if err != nil {
				return err
			}
			eventFilter, err := voxdiscord.ParseEventFilter(eventsFlag)
			if err != nil {
				return err
			}
			var slashDefs []serve.SlashCommandSpec
			if slashFile != "" {
				slashDefs, err = serve.LoadSlashCommands(slashFile)
				if err != nil {
					return err
				}
			}
			daemon := serve.NewDaemon(serve.Options{
				Token:         token,
				ServerFilter:  serverFilter,
				ChannelFilter: channelFilter,
				EventFilter:   eventFilter,
				IncludeSelf:   !noIncludeSelf,
				SlashCommands: slashDefs,
				Status:        status,
				ActivityType:  activity,
				ActivityText:  actText,
				VoiceConfig:   app.Store.VoiceConfig(),
				In:            app.In,
				Out:           app.Out,
				Logger:        app.Logger,
			})
			return daemon.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&serverFilter, "server", "", "Filter events by server name or ID")
	cmd.Flags().StringVar(&channelFilter, "channel", "", "Filter events by channel name or ID")
	cmd.Flags().StringVar(&eventsFlag, "events", "", "Comma-separated event categories")
	cmd.Flags().BoolVar(&noIncludeSelf, "no-include-self", false, "Exclude the bot's own messages from message events")
	cmd.Flags().StringVar(&slashFile, "slash-commands", "", "Path to JSON file defining slash commands to sync")
	cmd.Flags().StringVar(&status, "status", "online", "Initial bot status: online, idle, dnd, invisible")
	cmd.Flags().StringVar(&activity, "activity", "", "Activity type: playing, watching, listening, competing")
	cmd.Flags().StringVar(&actText, "activity-text", "", "Activity display text")
	return cmd
}

func newScheduleCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "schedule",
		Short:   "Schedule recurring vox commands on wall-clock times or intervals",
		GroupID: "realtime",
	}

	var (
		addAction, addTime, addTZ, addEvery string
		addCount                            int
		addForce                            bool
	)
	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create or replace a recurring vox command schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			name := args[0]
			if (addTime == "") == (addEvery == "") {
				return output.UsageError("specify exactly one of --time HH:MM or --every <duration>")
			}
			if addTZ != "" && addTime == "" {
				return output.UsageError("--tz can only be used with --time")
			}
			var countPtr *int
			if cmd.Flags().Changed("count") {
				if addCount < 1 {
					return output.UsageError("--count must be >= 1")
				}
				c := addCount
				countPtr = &c
			}
			argv, err := scheduler.ParseAction(addAction, app.ValidateCommandArgv)
			if err != nil {
				return err
			}

			entry := scheduler.Entry{
				Name:      name,
				Action:    addAction,
				Argv:      argv,
				Enabled:   true,
				Count:     countPtr,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			if addTime != "" {
				_, _, _, tz, err := scheduler.ParseWallClock(addTime, addTZ)
				if err != nil {
					return err
				}
				entry.Time = addTime
				entry.TZ = tz
			} else {
				seconds, err := scheduler.ParseInterval(addEvery)
				if err != nil {
					return err
				}
				entry.Every = seconds
				entry.EveryHuman = addEvery
			}

			store := scheduler.NewStore(app.Store.SchedulesPath())
			entries, err := store.Load()
			if err != nil {
				return err
			}
			replaced := false
			for i := range entries {
				if entries[i].Name == name {
					if !addForce {
						return output.UsageError(fmt.Sprintf("schedule %q already exists (use --force to replace)", name))
					}
					entries[i] = entry
					replaced = true
					break
				}
			}
			if !replaced {
				entries = append(entries, entry)
			}
			if err := store.Save(entries); err != nil {
				return err
			}
			_ = app.Security.Audit.Record("schedule add", map[string]any{"name": name, "action": addAction}, "ok", "")
			return app.Out.Print(entry, "Added schedule: "+entry.Describe())
		},
	}
	addCmd.Flags().StringVar(&addAction, "action", "", "vox command line to execute (required)")
	_ = addCmd.MarkFlagRequired("action")
	addCmd.Flags().StringVar(&addTime, "time", "", "Daily 24-hour wall-clock time (HH:MM)")
	addCmd.Flags().StringVar(&addTZ, "tz", "", "IANA timezone for --time (default UTC)")
	addCmd.Flags().StringVar(&addEvery, "every", "", "Fixed interval (e.g. 30s, 15m, 2h, 1d; min 30s)")
	addCmd.Flags().IntVar(&addCount, "count", 0, "Stop after N executions")
	addCmd.Flags().BoolVar(&addForce, "force", false, "Overwrite an existing schedule with the same name")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List configured schedules",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			store := scheduler.NewStore(app.Store.SchedulesPath())
			entries, err := store.Load()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return app.Out.Print(entries, "No schedules configured.")
			}
			lines := make([]string, len(entries))
			for i, e := range entries {
				lines[i] = e.Describe()
			}
			return app.Out.Print(entries, strings.Join(lines, "\n"))
		},
	}

	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a schedule by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if err := app.ConfirmDestructive("schedule remove", name); err != nil {
				return err
			}
			store := scheduler.NewStore(app.Store.SchedulesPath())
			entries, err := store.Load()
			if err != nil {
				return err
			}
			filtered := make([]scheduler.Entry, 0, len(entries))
			found := false
			for _, e := range entries {
				if e.Name == name {
					found = true
					continue
				}
				filtered = append(filtered, e)
			}
			if !found {
				return output.NewError("not_found", fmt.Sprintf("schedule not found: %s", name))
			}
			if err := store.Save(filtered); err != nil {
				return err
			}
			_ = app.Security.Audit.Record("schedule remove", map[string]any{"name": name}, "ok", "")
			return app.Out.Print(map[string]any{"name": name, "removed": true}, fmt.Sprintf("Removed schedule %q", name))
		},
	}

	executeSubcommand := func(ctx context.Context, argv []string) error {
		subApp := &App{
			Token:     app.Token,
			UseJSON:   app.UseJSON,
			Yes:       true,
			Profile:   app.Profile,
			LogLevel:  app.LogLevel,
			LogFormat: app.LogFormat,
			NoColor:   app.NoColor,
			ConfigDir: app.ConfigDir,
			In:        app.In,
			Stdout:    app.Stdout,
			Stderr:    app.Stderr,
		}
		subRoot := NewRootCommand(subApp)
		subRoot.SetArgs(argv)
		subRoot.SetContext(ctx)
		return subRoot.Execute()
	}

	runNowCmd := &cobra.Command{
		Use:   "run-now <name>",
		Short: "Execute a configured schedule immediately once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			store := scheduler.NewStore(app.Store.SchedulesPath())
			entries, err := store.Load()
			if err != nil {
				return err
			}
			var target *scheduler.Entry
			for i := range entries {
				if entries[i].Name == args[0] {
					target = &entries[i]
					break
				}
			}
			if target == nil {
				return output.NewError("not_found", fmt.Sprintf("schedule not found: %s", args[0]))
			}
			runErr := executeSubcommand(cmd.Context(), target.Argv)
			ok := runErr == nil
			detail := ""
			status := "ok"
			if runErr != nil {
				detail = runErr.Error()
				status = "failed: " + detail
			}
			_ = store.UpdateLastRun(target.Name, status, time.Now().UTC())
			_ = app.Security.Audit.Record("schedule run-now", map[string]any{"name": target.Name, "result": status}, status, "")
			plain := fmt.Sprintf("%s: ok", target.Name)
			if !ok {
				plain = fmt.Sprintf("%s: failed — %s", target.Name, detail)
			}
			if err := app.Out.Print(map[string]any{"name": target.Name, "ok": ok, "detail": detail}, plain); err != nil {
				return err
			}
			if !ok {
				return runErr
			}
			return nil
		},
	}

	var runNames []string
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run enabled schedules in the foreground until Ctrl-C",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			runner := &scheduler.Runner{
				Store:   scheduler.NewStore(app.Store.SchedulesPath()),
				Audit:   app.Security.Audit,
				Logger:  app.Logger,
				Execute: executeSubcommand,
				OnEvent: func(msg string) { app.Out.Note("%s", msg) },
			}
			err := runner.Run(cmd.Context(), runNames)
			app.Out.Note("Scheduler stopped.")
			return err
		},
	}
	runCmd.Flags().StringArrayVar(&runNames, "name", nil, "Only run the named schedule(s) (repeatable)")

	group.AddCommand(addCmd, listCmd, removeCmd, runNowCmd, runCmd)
	return group
}

func newInteractCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "interact",
		Short:   "Send interactive modals, multi-step workflows, and persistent dashboards",
		GroupID: "realtime",
	}

	var modalFields []string
	var modalChannel, modalServer string
	modalCmd := &cobra.Command{
		Use:   "modal <title>",
		Short: "Send a button message that triggers an interactive modal form",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := args[0]
			specs := make([]interact.ModalFieldSpec, 0, len(modalFields))
			for _, raw := range modalFields {
				spec, err := interact.ParseModalFieldFlag(raw)
				if err != nil {
					return err
				}
				specs = append(specs, spec)
			}
			customID := "modal:cli_" + interact.Slugify(title)
			if len(specs) > 0 {
				if _, err := interact.BuildModal(title, customID, specs); err != nil {
					return err
				}
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, modalChannel)
				if err != nil {
					return err
				}
				msg, err := client.CreateMessage(ch.ID(), disgodiscord.MessageCreate{
					Content: fmt.Sprintf("Click the button below to open the **%s** form.", title),
					Components: []disgodiscord.LayoutComponent{
						disgodiscord.NewActionRow(disgodiscord.NewPrimaryButton(title, customID)),
					},
				})
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(
					map[string]any{"message_id": msg.ID.String(), "channel_id": ch.ID().String(), "title": title, "fields": specs},
					fmt.Sprintf("Sent modal trigger '%s' to #%s (message %s)", title, ch.Name(), msg.ID),
				)
			})
		},
	}
	modalCmd.Flags().StringArrayVarP(&modalFields, "field", "f", nil, "Modal field Label[:short|paragraph[:required|optional]] (repeatable)")
	modalCmd.Flags().StringVar(&modalChannel, "channel", "", "Target channel name or ID (required)")
	_ = modalCmd.MarkFlagRequired("channel")
	modalCmd.Flags().StringVar(&modalServer, "server", "", "Optional server scope")

	workflowGroup := &cobra.Command{Use: "workflow", Short: "Manage interactive workflows"}
	var wfChannel, wfUser string
	wfStartCmd := &cobra.Command{
		Use:   "start <definition_file>",
		Short: "Start a multi-step workflow from a JSON definition file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var def interact.WorkflowDefinition
			if err := json.Unmarshal(raw, &def); err != nil {
				return output.UsageError(fmt.Sprintf("parse workflow %s: %v", args[0], err))
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, wfChannel)
				if err != nil {
					return err
				}
				engine := interact.NewEngine(client, app.Logger)
				defer engine.Close()
				key, err := engine.WorkflowStart(ch.ID(), wfUser, def)
				if err != nil {
					return err
				}
				return app.Out.Print(map[string]any{"workflow_key": key}, fmt.Sprintf("Started workflow %s", key))
			})
		},
	}
	wfStartCmd.Flags().StringVar(&wfChannel, "channel", "", "Target channel name or ID (required)")
	_ = wfStartCmd.MarkFlagRequired("channel")
	wfStartCmd.Flags().StringVar(&wfUser, "user", "", "Target user ID (required)")
	_ = wfStartCmd.MarkFlagRequired("user")
	workflowGroup.AddCommand(wfStartCmd)

	dashGroup := &cobra.Command{Use: "dashboard", Short: "Create or delete persistent embed/Components-v2 dashboards"}
	var dashCreateCh, dashDelCh string
	dashCreateCmd := &cobra.Command{
		Use:   "create <spec_file>",
		Short: "Create a persistent dashboard (embed or Components v2 layout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var def interact.DashboardDefinition
			if err := json.Unmarshal(raw, &def); err != nil {
				return output.UsageError(fmt.Sprintf("parse dashboard %s: %v", args[0], err))
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, dashCreateCh)
				if err != nil {
					return err
				}
				engine := interact.NewEngine(client, app.Logger)
				defer engine.Close()
				id, err := engine.DashboardCreate(ch.ID(), def)
				if err != nil {
					return err
				}
				return app.Out.Print(map[string]any{"dashboard_id": id}, fmt.Sprintf("Created dashboard %s", id))
			})
		},
	}
	dashCreateCmd.Flags().StringVar(&dashCreateCh, "channel", "", "Target channel name or ID (required)")
	_ = dashCreateCmd.MarkFlagRequired("channel")

	dashDelCmd := &cobra.Command{
		Use:   "delete <dashboard_id>",
		Short: "Delete a dashboard message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ch, err := voxdiscord.ResolveChannel(client, dashDelCh)
				if err != nil {
					return err
				}
				engine := interact.NewEngine(client, app.Logger)
				defer engine.Close()
				if err := engine.DashboardDelete(args[0], ch.ID()); err != nil {
					return err
				}
				return app.Out.Print(map[string]any{"dashboard_id": args[0], "deleted": true}, fmt.Sprintf("Deleted dashboard %s", args[0]))
			})
		},
	}
	dashDelCmd.Flags().StringVar(&dashDelCh, "channel", "", "Channel name or ID (required)")
	_ = dashDelCmd.MarkFlagRequired("channel")
	dashGroup.AddCommand(dashCreateCmd, dashDelCmd)

	group.AddCommand(modalCmd, workflowGroup, dashGroup)
	return group
}

func newVoiceCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "voice",
		Short:   "Join voice channels, speak TTS, play audio, transcribe speech, and inspect voice state",
		GroupID: "realtime",
	}

	// Helper that opens a minimal voice-only Gateway session and waits for READY.
	withVoiceGateway := func(cmd *cobra.Command, fn func(client *bot.Client, engine *voice.Engine) error) error {
		token, err := app.ResolveToken(cmd)
		if err != nil {
			return err
		}
		ready := make(chan struct{}, 1)
		client, err := voxdiscord.NewGatewayClient(
			token,
			[]string{"voice"},
			app.Logger,
			bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagChannels, cache.FlagVoiceStates, cache.FlagMembers)),
			bot.WithEventListenerFunc(func(_ *events.Ready) {
				select {
				case ready <- struct{}{}:
				default:
				}
			}),
		)
		if err != nil {
			return err
		}
		if err := client.OpenGateway(cmd.Context()); err != nil {
			return voxdiscord.MapError(err)
		}
		engine := voice.NewEngine(client, app.Store.VoiceConfig(), app.Logger)
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			engine.Close(closeCtx)
			client.Close(closeCtx)
		}()

		select {
		case <-ready:
		case <-time.After(15 * time.Second):
			return output.NewError("gateway_timeout", "timed out waiting for Discord Gateway ready")
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		}
		return fn(client, engine)
	}

	resolveVoiceChannel := func(client *bot.Client, serverRef, channelRef string) (snowflake.ID, string, snowflake.ID, string, error) {
		var guildID snowflake.ID
		var guildName string
		if serverRef != "" {
			g, err := voxdiscord.ResolveGuild(client.Rest, serverRef)
			if err != nil {
				return 0, "", 0, "", err
			}
			guildID = g.ID
			guildName = g.Name
			ch, err := voxdiscord.ResolveChannelInGuild(client.Rest, g.ID, channelRef)
			if err != nil {
				return 0, "", 0, "", err
			}
			return guildID, guildName, ch.ID(), ch.Name(), nil
		}
		ch, err := voxdiscord.ResolveChannel(client.Rest, channelRef)
		if err != nil {
			return 0, "", 0, "", err
		}
		gch, ok := ch.(disgodiscord.GuildChannel)
		if !ok {
			return 0, "", 0, "", output.UsageError("not a guild voice channel")
		}
		gName := gch.GuildID().String()
		if g, ok := client.Caches.Guild(gch.GuildID()); ok {
			gName = g.Name
		}
		return gch.GuildID(), gName, ch.ID(), ch.Name(), nil
	}

	var joinServer string
	joinCmd := &cobra.Command{
		Use:   "join <channel>",
		Short: "Join a voice channel (one-shot connect check)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				gid, gname, cid, cname, err := resolveVoiceChannel(client, joinServer, args[0])
				if err != nil {
					return err
				}
				if _, err := engine.Connect(cmd.Context(), gid, cid); err != nil {
					return err
				}
				return app.Out.Print(
					map[string]any{"channel_id": cid.String(), "channel_name": cname, "guild_id": gid.String(), "guild_name": gname, "connected": true},
					fmt.Sprintf("Joined voice channel #%s in %s", cname, gname),
				)
			})
		},
	}
	joinCmd.Flags().StringVar(&joinServer, "server", "", "Scope lookup to one server")

	var leaveServer string
	leaveCmd := &cobra.Command{
		Use:   "leave",
		Short: "Leave the active voice channel in a server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				g, err := voxdiscord.ResolveGuild(client.Rest, leaveServer)
				if err != nil {
					return err
				}
				if err := engine.Disconnect(cmd.Context(), g.ID); err != nil {
					return err
				}
				return app.Out.Print(
					map[string]any{"guild_id": g.ID.String(), "guild_name": g.Name, "disconnected": true},
					fmt.Sprintf("Left voice in %s", g.Name),
				)
			})
		},
	}
	leaveCmd.Flags().StringVar(&leaveServer, "server", "", "Server name or ID")

	var speakServer, speakChannel, speakVoice string
	var speakSpeed float64
	speakCmd := &cobra.Command{
		Use:   "speak <text>",
		Short: "Synthesize text via TTS and play it in a voice channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if speakChannel == "" {
				return output.UsageError("specify --channel for one-shot `vox voice speak` (or use `vox serve` for persistent voice sessions)")
			}
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				gid, gname, cid, _, err := resolveVoiceChannel(client, speakServer, speakChannel)
				if err != nil {
					return err
				}
				if _, err := engine.Connect(cmd.Context(), gid, cid); err != nil {
					return err
				}
				if err := engine.Speak(cmd.Context(), gid, args[0], speakVoice, speakSpeed); err != nil {
					return err
				}
				return app.Out.Print(
					map[string]any{"guild_id": gid.String(), "guild_name": gname, "text": args[0], "voice": speakVoice, "speed": speakSpeed},
					fmt.Sprintf("Spoke in %s", gname),
				)
			})
		},
	}
	speakCmd.Flags().StringVar(&speakServer, "server", "", "Server name or ID")
	speakCmd.Flags().StringVar(&speakChannel, "channel", "", "Voice channel name or ID")
	speakCmd.Flags().StringVar(&speakVoice, "voice", "default", "TTS voice name")
	speakCmd.Flags().Float64Var(&speakSpeed, "speed", 1.0, "Speech speed multiplier")

	var playServer, playChannel string
	var playVolume float64
	playCmd := &cobra.Command{
		Use:   "play <source>",
		Short: "Play an audio file or URL in a voice channel via FFmpeg",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if playChannel == "" {
				return output.UsageError("specify --channel for one-shot `vox voice play` (or use `vox serve` for persistent sessions)")
			}
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				gid, gname, cid, _, err := resolveVoiceChannel(client, playServer, playChannel)
				if err != nil {
					return err
				}
				engine.UpdateConfig(map[string]any{"playback_volume": playVolume})
				if _, err := engine.Connect(cmd.Context(), gid, cid); err != nil {
					return err
				}
				if err := engine.Play(gid, args[0]); err != nil {
					return err
				}
				return app.Out.Print(
					map[string]any{"guild_id": gid.String(), "guild_name": gname, "source": args[0], "volume": playVolume},
					fmt.Sprintf("Playing %s in %s", args[0], gname),
				)
			})
		},
	}
	playCmd.Flags().StringVar(&playServer, "server", "", "Server name or ID")
	playCmd.Flags().StringVar(&playChannel, "channel", "", "Voice channel name or ID")
	playCmd.Flags().Float64Var(&playVolume, "volume", 1.0, "Playback volume (0.0-2.0)")

	makeVoiceControl := func(name, short string) *cobra.Command {
		var srv string
		c := &cobra.Command{
			Use:   name,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
					var gid snowflake.ID
					gname := srv
					if srv != "" {
						g, err := voxdiscord.ResolveGuild(client.Rest, srv)
						if err != nil {
							return err
						}
						gid, gname = g.ID, g.Name
					}
					switch name {
					case "stop":
						_ = engine.Stop(gid)
					case "pause":
						_ = engine.Pause(gid)
					case "resume":
						_ = engine.Resume(gid)
					}
					return app.Out.Print(map[string]any{"guild_id": gid.String(), "guild_name": gname, name: true}, fmt.Sprintf("Voice %s sent.", name))
				})
			},
		}
		c.Flags().StringVar(&srv, "server", "", "Server name or ID")
		return c
	}

	var listenServer, listenChannel string
	var listenDuration int
	var listenContinuous bool
	listenCmd := &cobra.Command{
		Use:   "listen",
		Short: "Listen to a voice channel and print finalized STT transcripts per speaker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listenChannel == "" {
				return output.UsageError("specify --channel to join and transcribe")
			}
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				gid, gname, cid, _, err := resolveVoiceChannel(client, listenServer, listenChannel)
				if err != nil {
					return err
				}
				var transcripts []map[string]any
				engine.SetEventHandler(func(ev map[string]any) {
					if ev["event"] == "voice_speech_detected" {
						transcripts = append(transcripts, ev)
						_ = app.Out.PrintStream(ev, fmt.Sprintf("[%v] %v", ev["user_id"], ev["text"]))
					}
				})
				if _, err := engine.Connect(cmd.Context(), gid, cid); err != nil {
					return err
				}
				if err := engine.ListenStart(gid); err != nil {
					return err
				}
				if listenDuration > 0 {
					select {
					case <-time.After(time.Duration(listenDuration) * time.Second):
					case <-cmd.Context().Done():
					}
				} else {
					<-cmd.Context().Done()
				}
				engine.ListenStop(gid)
				return app.Out.Print(
					map[string]any{"guild_id": gid.String(), "guild_name": gname, "transcriptions": transcripts, "count": len(transcripts)},
					fmt.Sprintf("Captured %d transcript(s) in %s.", len(transcripts), gname),
				)
			})
		},
	}
	listenCmd.Flags().StringVar(&listenServer, "server", "", "Server name or ID")
	listenCmd.Flags().StringVar(&listenChannel, "channel", "", "Voice channel name or ID")
	listenCmd.Flags().IntVar(&listenDuration, "duration", 0, "Duration in seconds (0 = until Ctrl-C)")
	listenCmd.Flags().BoolVar(&listenContinuous, "continuous", false, "Keep session alive across utterances")

	var capServer, capChannel, capOutDir string
	var capDuration int
	captureCmd := &cobra.Command{
		Use:   "capture",
		Short: "Debug helper: record raw 48 kHz stereo PCM WAV per speaker, bypassing STT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if capChannel == "" {
				return output.UsageError("specify --channel to capture audio from")
			}
			if capOutDir == "" {
				capOutDir = app.Store.DebugCaptureDir()
			}
			return withVoiceGateway(cmd, func(client *bot.Client, engine *voice.Engine) error {
				gid, gname, cid, _, err := resolveVoiceChannel(client, capServer, capChannel)
				if err != nil {
					return err
				}
				conn, err := engine.Connect(cmd.Context(), gid, cid)
				if err != nil {
					return err
				}
				recorder := voice.NewCaptureReceiver()
				conn.SetOpusFrameReceiver(recorder)
				select {
				case <-time.After(time.Duration(capDuration) * time.Second):
				case <-cmd.Context().Done():
				}
				recorder.Close()
				speakers, err := recorder.WriteWAVFiles(capOutDir)
				if err != nil {
					return err
				}
				return app.Out.Print(
					map[string]any{
						"guild_id":         gid.String(),
						"guild_name":       gname,
						"duration_seconds": capDuration,
						"output_dir":       capOutDir,
						"speakers":         speakers,
					},
					fmt.Sprintf("Captured %d speaker WAV file(s) in %s", len(speakers), capOutDir),
				)
			})
		},
	}
	captureCmd.Flags().StringVar(&capServer, "server", "", "Server name or ID")
	captureCmd.Flags().StringVar(&capChannel, "channel", "", "Voice channel name or ID")
	captureCmd.Flags().IntVar(&capDuration, "duration", 10, "Capture duration in seconds")
	captureCmd.Flags().StringVar(&capOutDir, "output-dir", "", "Output directory (default: ~/.vox/debug-capture)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List active voice connections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withVoiceGateway(cmd, func(_ *bot.Client, engine *voice.Engine) error {
				st := engine.Status()
				if len(st) == 0 {
					return app.Out.Print(st, "No active voice connections.")
				}
				lines := make([]string, len(st))
				for i, c := range st {
					lines[i] = fmt.Sprintf("guild:%s channel:%s playing=%v listening=%v", c.GuildID, c.ChannelID, c.IsPlaying, c.IsListening)
				}
				return app.Out.Print(st, strings.Join(lines, "\n"))
			})
		},
	}

	var whereServer string
	whereCmd := &cobra.Command{
		Use:   "where <user>",
		Short: "Find which voice channel a user is currently in",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.TrimPrefix(strings.TrimSpace(args[0]), "@")
			return withVoiceGateway(cmd, func(client *bot.Client, _ *voice.Engine) error {
				matches := make([]map[string]any, 0)
				lines := make([]string, 0)
				for guild := range client.Caches.Guilds() {
					if whereServer != "" && guild.ID.String() != whereServer && !strings.EqualFold(guild.Name, whereServer) {
						continue
					}
					for vs := range client.Caches.VoiceStates(guild.ID) {
						if vs.ChannelID == nil {
							continue
						}
						userName := vs.UserID.String()
						displayName := userName
						if member, ok := client.Caches.Member(guild.ID, vs.UserID); ok {
							userName = voxdiscord.FormatUserTag(member.User)
							displayName = member.EffectiveName()
						}
						if vs.UserID.String() != query && !strings.EqualFold(userName, query) && !strings.EqualFold(displayName, query) {
							continue
						}
						chName := vs.ChannelID.String()
						if ch, ok := client.Caches.Channel(*vs.ChannelID); ok {
							chName = ch.Name()
						}
						matches = append(matches, map[string]any{
							"user_id":      vs.UserID.String(),
							"user_name":    userName,
							"guild_id":     guild.ID.String(),
							"guild_name":   guild.Name,
							"channel_id":   vs.ChannelID.String(),
							"channel_name": chName,
							"self_mute":    vs.SelfMute,
							"self_deaf":    vs.SelfDeaf,
							"mute":         vs.GuildMute,
							"deaf":         vs.GuildDeaf,
						})
						lines = append(lines, fmt.Sprintf("%s is in #%s (%s)", userName, chName, guild.Name))
					}
				}
				plain := fmt.Sprintf("User %s is not in any visible voice channel.", args[0])
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(matches, plain)
			})
		},
	}
	whereCmd.Flags().StringVar(&whereServer, "server", "", "Scope lookup to one server")

	var membersServer string
	membersCmd := &cobra.Command{
		Use:   "members <channel>",
		Short: "List members currently connected to a voice channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVoiceGateway(cmd, func(client *bot.Client, _ *voice.Engine) error {
				gid, _, cid, cname, err := resolveVoiceChannel(client, membersServer, args[0])
				if err != nil {
					return err
				}
				members := make([]map[string]any, 0)
				lines := make([]string, 0)
				for vs := range client.Caches.VoiceStates(gid) {
					if vs.ChannelID == nil || *vs.ChannelID != cid {
						continue
					}
					uName := vs.UserID.String()
					dName := uName
					if m, ok := client.Caches.Member(gid, vs.UserID); ok {
						uName = voxdiscord.FormatUserTag(m.User)
						dName = m.EffectiveName()
					}
					members = append(members, map[string]any{
						"user_id":      vs.UserID.String(),
						"user_name":    uName,
						"display_name": dName,
						"self_mute":    vs.SelfMute,
						"self_deaf":    vs.SelfDeaf,
						"mute":         vs.GuildMute,
						"deaf":         vs.GuildDeaf,
					})
					lines = append(lines, fmt.Sprintf("%s (ID: %s)", dName, vs.UserID))
				}
				plain := fmt.Sprintf("No members in #%s.", cname)
				if len(lines) > 0 {
					plain = strings.Join(lines, "\n")
				}
				return app.Out.Print(map[string]any{
					"channel_id":   cid.String(),
					"channel_name": cname,
					"guild_id":     gid.String(),
					"count":        len(members),
					"members":      members,
				}, plain)
			})
		},
	}
	membersCmd.Flags().StringVar(&membersServer, "server", "", "Scope lookup to one server")

	// voice move uses REST only (no Gateway session needed).
	var moveReason string
	moveCmd := &cobra.Command{
		Use:   "move <server> <member> <channel>",
		Short: "Move a connected member to another voice or stage channel",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				app.Security.Limiter.Wait(func(d time.Duration) { app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds()) })
				guild, err := voxdiscord.ResolveGuild(client, args[0])
				if err != nil {
					return err
				}
				member, err := voxdiscord.ResolveMember(client, guild.ID, args[1])
				if err != nil {
					return err
				}
				ch, err := voxdiscord.ResolveChannelInGuild(client, guild.ID, args[2])
				if err != nil {
					return err
				}
				if ch.Type() != disgodiscord.ChannelTypeGuildVoice && ch.Type() != disgodiscord.ChannelTypeGuildStageVoice {
					return output.UsageError(fmt.Sprintf("#%s is not a voice or stage channel", ch.Name()))
				}
				targetID := ch.ID()
				var opts []rest.RequestOpt
				if moveReason != "" {
					opts = append(opts, rest.WithReason(moveReason))
				}
				if _, err := client.UpdateMember(guild.ID, member.User.ID, disgodiscord.MemberUpdate{
					ChannelID: omit.New(&targetID),
				}, opts...); err != nil {
					return voxdiscord.MapError(err)
				}
				tag := voxdiscord.FormatUserTag(member.User)
				_ = app.Security.Audit.Record("voice move", map[string]any{"server": args[0], "member": tag, "channel": ch.Name()}, "ok", "")
				return app.Out.Print(
					map[string]any{"member": tag, "member_id": member.User.ID.String(), "channel": ch.Name(), "channel_id": ch.ID().String(), "moved": true},
					fmt.Sprintf("Moved %s to #%s", tag, ch.Name()),
				)
			})
		},
	}
	moveCmd.Flags().StringVar(&moveReason, "reason", "", "Audit log reason")

	group.AddCommand(
		joinCmd, leaveCmd, speakCmd, playCmd,
		makeVoiceControl("stop", "Stop active voice playback"),
		makeVoiceControl("pause", "Pause active voice playback"),
		makeVoiceControl("resume", "Resume paused voice playback"),
		listenCmd, captureCmd, statusCmd, whereCmd, membersCmd, moveCmd,
	)
	return group
}
