package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/interact"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/voice"
)

// Options configures a `vox serve` daemon run.
type Options struct {
	Token         string
	ServerFilter  string
	ChannelFilter string
	EventFilter   map[string]bool
	IncludeSelf   bool
	SlashCommands []SlashCommandSpec
	Status        string
	ActivityType  string
	ActivityText  string
	VoiceConfig   map[string]any
	In            io.Reader
	Out           *output.Writer
	Logger        *slog.Logger
}

type storedInteraction struct {
	id            snowflake.ID
	applicationID snowflake.ID
	token         string
	responded     bool
	deferred      bool
	mutex         sync.Mutex
	autoDeferStop context.CancelFunc
}

type streamState struct {
	id          string
	channelID   snowflake.ID
	messageID   snowflake.ID
	buffer      strings.Builder
	lastFlushed string
	done        bool
	cancelFlush context.CancelFunc
	mutex       sync.Mutex
}

// Daemon runs the persistent Discord Gateway connection and JSONL command loop.
type Daemon struct {
	options  Options
	client   *bot.Client
	voice    *voice.Engine
	interact *interact.Engine

	botID snowflake.ID

	mutex        sync.Mutex
	typingTasks  map[snowflake.ID]context.CancelFunc
	streams      map[string]*streamState
	interactions map[string]*storedInteraction
}

// NewDaemon constructs a Daemon ready to be started via Run.
func NewDaemon(options Options) *Daemon {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.In == nil {
		options.In = os.Stdin
	}
	return &Daemon{
		options:      options,
		typingTasks:  make(map[snowflake.ID]context.CancelFunc),
		streams:      make(map[string]*streamState),
		interactions: make(map[string]*storedInteraction),
	}
}

// LoadSlashCommands reads a --slash-commands JSON definition file.
func LoadSlashCommands(path string) ([]SlashCommandSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read slash commands %s: %w", path, err)
	}
	var specs []SlashCommandSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		return nil, output.UsageError(fmt.Sprintf("parse slash commands %s: %v", path, err))
	}
	return specs, nil
}

// Run connects to Discord's Gateway, emits events on stdout, and dispatches
// stdin JSONL actions until ctx is cancelled or stdin closes.
func (daemon *Daemon) Run(ctx context.Context) error {
	features := voxdiscord.GatewayFeaturesForEvents(daemon.options.EventFilter)
	if !slices.Contains(features, "voice") {
		features = append(features, "voice")
	}

	readyChan := make(chan struct{}, 1)
	client, err := voxdiscord.NewGatewayClient(
		daemon.options.Token,
		features,
		daemon.options.Logger,
		bot.WithCacheConfigOpts(
			cache.WithCaches(
				cache.FlagGuilds,
				cache.FlagChannels,
				cache.FlagRoles,
				cache.FlagVoiceStates,
				cache.FlagMembers,
				cache.FlagMessages,
			),
		),
		bot.WithEventListenerFunc(func(event *events.Ready) {
			daemon.onReady(ctx, event, readyChan)
		}),
		bot.WithEventListenerFunc(func(event *events.MessageCreate) {
			daemon.onMessageCreate(event)
		}),
		bot.WithEventListenerFunc(func(event *events.MessageUpdate) {
			daemon.onMessageUpdate(event)
		}),
		bot.WithEventListenerFunc(func(event *events.MessageDelete) {
			daemon.onMessageDelete(event)
		}),
		bot.WithEventListenerFunc(func(event *events.MessageReactionAdd) {
			daemon.onReactionAdd(event)
		}),
		bot.WithEventListenerFunc(func(event *events.MessageReactionRemove) {
			daemon.onReactionRemove(event)
		}),
		bot.WithEventListenerFunc(func(event *events.GuildMemberJoin) {
			daemon.onMemberJoin(event)
		}),
		bot.WithEventListenerFunc(func(event *events.GuildMemberLeave) {
			daemon.onMemberLeave(event)
		}),
		bot.WithEventListenerFunc(func(event *events.GuildVoiceStateUpdate) {
			daemon.onVoiceStateUpdate(event)
		}),
		bot.WithEventListenerFunc(func(event *events.ApplicationCommandInteractionCreate) {
			daemon.onSlashCommand(event)
		}),
		bot.WithEventListenerFunc(func(event *events.ComponentInteractionCreate) {
			daemon.onComponentInteraction(event)
		}),
		bot.WithEventListenerFunc(func(event *events.ModalSubmitInteractionCreate) {
			daemon.onModalSubmit(event)
		}),
		bot.WithEventListenerFunc(func(_ *events.Resumed) {
			daemon.emit(map[string]any{"event": "resumed"})
		}),
	)
	if err != nil {
		return err
	}

	daemon.client = client
	daemon.voice = voice.NewEngine(client, daemon.options.VoiceConfig, daemon.options.Logger)
	daemon.voice.SetEventHandler(daemon.emit)
	daemon.interact = interact.NewEngine(client.Rest, daemon.options.Logger)
	daemon.interact.SetEventHandler(daemon.emit)

	if err := client.OpenGateway(ctx); err != nil {
		return voxdiscord.MapError(err)
	}
	defer daemon.cleanup()

	select {
	case <-readyChan:
	case <-ctx.Done():
		daemon.emit(map[string]any{"event": "shutdown"})
		return nil
	}

	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		daemon.readStdin(ctx)
	}()

	select {
	case <-ctx.Done():
		daemon.emit(map[string]any{"event": "shutdown"})
	case <-stdinDone:
	}
	return nil
}

func (daemon *Daemon) cleanup() {
	daemon.mutex.Lock()
	for _, cancel := range daemon.typingTasks {
		cancel()
	}
	clear(daemon.typingTasks)
	for _, stream := range daemon.streams {
		if stream.cancelFlush != nil {
			stream.cancelFlush()
		}
	}
	clear(daemon.streams)
	for _, inter := range daemon.interactions {
		if inter.autoDeferStop != nil {
			inter.autoDeferStop()
		}
	}
	clear(daemon.interactions)
	daemon.mutex.Unlock()

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if daemon.voice != nil {
		daemon.voice.Close(closeCtx)
	}
	if daemon.interact != nil {
		daemon.interact.Close()
	}
	if daemon.client != nil {
		daemon.client.Close(closeCtx)
	}
}

func (daemon *Daemon) emit(payload map[string]any) {
	if daemon.options.Out == nil {
		return
	}
	if err := daemon.options.Out.PrintJSONL(payload); err != nil {
		daemon.options.Logger.Debug("stdout write error", "error", err)
	}
}

func (daemon *Daemon) readStdin(ctx context.Context) {
	scanner := bufio.NewScanner(daemon.options.In)
	// Support large payloads (embeds, dashboards, multi-KB messages) up to 4 MB per line.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		req, err := ParseRequest([]byte(line))
		if err != nil {
			preview := line
			if len(preview) > 100 {
				preview = preview[:100]
			}
			daemon.emit(map[string]any{
				"event":   "error",
				"message": "Invalid JSON: " + preview,
			})
			continue
		}
		response := daemon.Dispatch(ctx, req)
		response["event"] = "response"
		if req.ReqID != nil {
			response["req_id"] = req.ReqID
		}
		daemon.emit(response)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		daemon.emit(map[string]any{
			"event":   "error",
			"message": err.Error(),
		})
	}
}

func (daemon *Daemon) eventEnabled(category string) bool {
	if len(daemon.options.EventFilter) == 0 {
		return true
	}
	return daemon.options.EventFilter[category]
}

func (daemon *Daemon) matchesGuildChannel(guildID *snowflake.ID, channelID snowflake.ID) bool {
	if guildID == nil {
		return true // DMs bypass server/channel filters.
	}
	if daemon.options.ServerFilter != "" {
		guild, ok := daemon.client.Caches.Guild(*guildID)
		matched := guildID.String() == daemon.options.ServerFilter || (ok && strings.EqualFold(guild.Name, daemon.options.ServerFilter))
		if !matched {
			return false
		}
	}
	if daemon.options.ChannelFilter != "" {
		want := strings.TrimPrefix(daemon.options.ChannelFilter, "#")
		ch, ok := daemon.client.Caches.Channel(channelID)
		matched := channelID.String() == want || (ok && strings.EqualFold(ch.Name(), want))
		if !matched {
			return false
		}
	}
	return true
}

func (daemon *Daemon) onReady(ctx context.Context, event *events.Ready, readyChan chan<- struct{}) {
	daemon.botID = event.User.ID
	daemon.emit(map[string]any{
		"event":    "ready",
		"bot_id":   event.User.ID.String(),
		"bot_name": event.User.Username,
	})

	if err := daemon.setPresence(ctx, daemon.options.Status, daemon.options.ActivityType, daemon.options.ActivityText); err != nil {
		daemon.options.Logger.Warn("failed to set initial presence", "error", err)
	}

	if len(daemon.options.SlashCommands) > 0 {
		daemon.registerSlashCommands(event)
	}

	select {
	case readyChan <- struct{}{}:
	default:
	}
}

func (daemon *Daemon) setPresence(ctx context.Context, status, activityType, activityText string) error {
	opts := make([]gateway.PresenceOpt, 0, 2)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "idle":
		opts = append(opts, gateway.WithOnlineStatus(disgodiscord.OnlineStatusIdle))
	case "dnd":
		opts = append(opts, gateway.WithOnlineStatus(disgodiscord.OnlineStatusDND))
	case "invisible":
		opts = append(opts, gateway.WithOnlineStatus(disgodiscord.OnlineStatusInvisible))
	default:
		opts = append(opts, gateway.WithOnlineStatus(disgodiscord.OnlineStatusOnline))
	}
	if activityType != "" && activityText != "" {
		switch strings.ToLower(strings.TrimSpace(activityType)) {
		case "playing":
			opts = append(opts, gateway.WithPlayingActivity(activityText))
		case "watching":
			opts = append(opts, gateway.WithWatchingActivity(activityText))
		case "listening":
			opts = append(opts, gateway.WithListeningActivity(activityText))
		case "competing":
			opts = append(opts, gateway.WithCompetingActivity(activityText))
		}
	}
	return daemon.client.SetPresence(ctx, opts...)
}

func (daemon *Daemon) registerSlashCommands(ready *events.Ready) {
	creates := make([]disgodiscord.ApplicationCommandCreate, 0, len(daemon.options.SlashCommands))
	for _, spec := range daemon.options.SlashCommands {
		desc := spec.Description
		if desc == "" {
			desc = spec.Name
		}
		options := make([]disgodiscord.ApplicationCommandOption, 0, len(spec.Params))
		for _, param := range spec.Params {
			paramDesc := param.Description
			if paramDesc == "" {
				paramDesc = param.Name
			}
			required := true
			if param.Required != nil {
				required = *param.Required
			}
			switch strings.ToLower(param.Type) {
			case "integer":
				options = append(options, disgodiscord.ApplicationCommandOptionInt{
					Name:        param.Name,
					Description: paramDesc,
					Required:    required,
				})
			case "number":
				options = append(options, disgodiscord.ApplicationCommandOptionFloat{
					Name:        param.Name,
					Description: paramDesc,
					Required:    required,
				})
			case "boolean":
				options = append(options, disgodiscord.ApplicationCommandOptionBool{
					Name:        param.Name,
					Description: paramDesc,
					Required:    required,
				})
			default:
				options = append(options, disgodiscord.ApplicationCommandOptionString{
					Name:        param.Name,
					Description: paramDesc,
					Required:    required,
				})
			}
		}
		creates = append(creates, disgodiscord.SlashCommandCreate{
			Name:        spec.Name,
			Description: desc,
			Options:     options,
		})
	}

	syncedGuilds := 0
	for _, guild := range ready.Guilds {
		if _, err := daemon.client.Rest.SetGuildCommands(daemon.client.ApplicationID, guild.ID, creates); err != nil {
			daemon.emit(map[string]any{
				"event":   "error",
				"message": fmt.Sprintf("Slash command registration failed for guild %s: %v", guild.ID, err),
			})
			continue
		}
		syncedGuilds++
	}
	daemon.emit(map[string]any{
		"event":  "slash_commands_synced",
		"count":  len(creates),
		"guilds": syncedGuilds,
	})
}

func (daemon *Daemon) onMessageCreate(event *events.MessageCreate) {
	if !daemon.eventEnabled("messages") {
		return
	}
	msg := event.Message
	if msg.Author.ID == daemon.botID {
		if !daemon.options.IncludeSelf {
			return
		}
	} else if msg.Author.Bot {
		return
	}
	if !daemon.matchesGuildChannel(event.GuildID, event.ChannelID) {
		return
	}

	serverName, serverID := daemon.guildMeta(event.GuildID)
	channelName := daemon.channelName(event.ChannelID, event.GuildID == nil)

	mentionsBot := false
	for _, mention := range msg.Mentions {
		if mention.ID == daemon.botID {
			mentionsBot = true
			break
		}
	}
	attachments := make([]map[string]any, 0, len(msg.Attachments))
	for _, att := range msg.Attachments {
		attachments = append(attachments, map[string]any{
			"filename": att.Filename,
			"url":      att.URL,
			"size":     att.Size,
		})
	}
	var replyTo any
	if msg.MessageReference != nil && msg.MessageReference.MessageID != nil {
		replyTo = msg.MessageReference.MessageID.String()
	}

	daemon.emit(map[string]any{
		"event":        "message",
		"server":       serverName,
		"server_id":    serverID,
		"channel":      channelName,
		"channel_id":   event.ChannelID.String(),
		"author":       voxdiscord.FormatUserTag(msg.Author),
		"author_id":    msg.Author.ID.String(),
		"is_bot":       msg.Author.Bot,
		"content":      msg.Content,
		"timestamp":    msg.CreatedAt.UTC().Format(time.RFC3339),
		"message_id":   msg.ID.String(),
		"mentions_bot": mentionsBot,
		"is_dm":        event.GuildID == nil,
		"attachments":  attachments,
		"reply_to":     replyTo,
	})
}

func (daemon *Daemon) onMessageUpdate(event *events.MessageUpdate) {
	if !daemon.eventEnabled("edits") || event.Message.Author.Bot {
		return
	}
	if !daemon.matchesGuildChannel(event.GuildID, event.ChannelID) {
		return
	}
	serverName, serverID := daemon.guildMeta(event.GuildID)
	channelName := daemon.channelName(event.ChannelID, event.GuildID == nil)
	var oldContent any
	if event.OldMessage.Content != "" {
		oldContent = event.OldMessage.Content
	}
	ts := event.Message.CreatedAt
	if event.Message.EditedTimestamp != nil {
		ts = *event.Message.EditedTimestamp
	}
	daemon.emit(map[string]any{
		"event":       "message_edit",
		"server":      serverName,
		"server_id":   serverID,
		"channel":     channelName,
		"channel_id":  event.ChannelID.String(),
		"author":      voxdiscord.FormatUserTag(event.Message.Author),
		"author_id":   event.Message.Author.ID.String(),
		"message_id":  event.MessageID.String(),
		"old_content": oldContent,
		"new_content": event.Message.Content,
		"timestamp":   ts.UTC().Format(time.RFC3339),
	})
}

func (daemon *Daemon) onMessageDelete(event *events.MessageDelete) {
	if !daemon.eventEnabled("deletes") {
		return
	}
	if !daemon.matchesGuildChannel(event.GuildID, event.ChannelID) {
		return
	}
	serverName, serverID := daemon.guildMeta(event.GuildID)
	channelName := daemon.channelName(event.ChannelID, event.GuildID == nil)
	var author, authorID, content any
	if event.Message.Author.ID != 0 {
		author = voxdiscord.FormatUserTag(event.Message.Author)
		authorID = event.Message.Author.ID.String()
	}
	if event.Message.Content != "" {
		content = event.Message.Content
	}
	daemon.emit(map[string]any{
		"event":      "message_delete",
		"server":     serverName,
		"server_id":  serverID,
		"channel":    channelName,
		"channel_id": event.ChannelID.String(),
		"author":     author,
		"author_id":  authorID,
		"message_id": event.MessageID.String(),
		"content":    content,
	})
}

func (daemon *Daemon) onReactionAdd(event *events.MessageReactionAdd) {
	if !daemon.eventEnabled("reactions") {
		return
	}
	if !daemon.matchesGuildChannel(event.GuildID, event.ChannelID) {
		return
	}
	serverName, _ := daemon.guildMeta(event.GuildID)
	channelName := daemon.channelName(event.ChannelID, event.GuildID == nil)
	userName := event.UserID.String()
	if event.Member != nil {
		userName = voxdiscord.FormatUserTag(event.Member.User)
	}
	daemon.emit(map[string]any{
		"event":      "reaction_add",
		"server":     serverName,
		"channel":    channelName,
		"channel_id": event.ChannelID.String(),
		"message_id": event.MessageID.String(),
		"emoji":      event.Emoji.Reaction(),
		"user":       userName,
		"user_id":    event.UserID.String(),
	})
}

func (daemon *Daemon) onReactionRemove(event *events.MessageReactionRemove) {
	if !daemon.eventEnabled("reactions") {
		return
	}
	if !daemon.matchesGuildChannel(event.GuildID, event.ChannelID) {
		return
	}
	serverName, _ := daemon.guildMeta(event.GuildID)
	channelName := daemon.channelName(event.ChannelID, event.GuildID == nil)
	daemon.emit(map[string]any{
		"event":      "reaction_remove",
		"server":     serverName,
		"channel":    channelName,
		"channel_id": event.ChannelID.String(),
		"message_id": event.MessageID.String(),
		"emoji":      event.Emoji.Reaction(),
		"user":       event.UserID.String(),
		"user_id":    event.UserID.String(),
	})
}

func (daemon *Daemon) onMemberJoin(event *events.GuildMemberJoin) {
	if !daemon.eventEnabled("members") {
		return
	}
	gid := event.GuildID
	serverName, _ := daemon.guildMeta(&gid)
	daemon.emit(map[string]any{
		"event":     "member_join",
		"server":    serverName,
		"server_id": event.GuildID.String(),
		"member":    voxdiscord.FormatUserTag(event.Member.User),
		"member_id": event.Member.User.ID.String(),
	})
}

func (daemon *Daemon) onMemberLeave(event *events.GuildMemberLeave) {
	if !daemon.eventEnabled("members") {
		return
	}
	gid := event.GuildID
	serverName, _ := daemon.guildMeta(&gid)
	daemon.emit(map[string]any{
		"event":     "member_remove",
		"server":    serverName,
		"server_id": event.GuildID.String(),
		"member":    voxdiscord.FormatUserTag(event.User),
		"member_id": event.User.ID.String(),
	})
}

func (daemon *Daemon) onVoiceStateUpdate(event *events.GuildVoiceStateUpdate) {
	if !daemon.eventEnabled("voice") {
		return
	}
	before := event.OldVoiceState.ChannelID
	after := event.VoiceState.ChannelID
	if before == nil && after == nil {
		return
	}
	gid := event.VoiceState.GuildID
	serverName, _ := daemon.guildMeta(&gid)
	memberName := event.VoiceState.UserID.String()
	if event.Member.User.ID != 0 {
		memberName = voxdiscord.FormatUserTag(event.Member.User)
	}
	payload := map[string]any{
		"event":     "voice_state",
		"server":    serverName,
		"server_id": gid.String(),
		"member":    memberName,
		"member_id": event.VoiceState.UserID.String(),
	}

	switch {
	case before == nil && after != nil:
		payload["action"] = "joined"
		payload["channel"] = daemon.channelName(*after, false)
		payload["channel_id"] = after.String()
	case before != nil && after == nil:
		payload["action"] = "left"
		payload["channel"] = daemon.channelName(*before, false)
		payload["channel_id"] = before.String()
	case *before != *after:
		payload["action"] = "moved"
		payload["from_channel"] = daemon.channelName(*before, false)
		payload["from_channel_id"] = before.String()
		payload["channel"] = daemon.channelName(*after, false)
		payload["channel_id"] = after.String()
	default:
		payload["action"] = "updated"
		payload["channel"] = daemon.channelName(*after, false)
		payload["channel_id"] = after.String()
		payload["self_mute"] = event.VoiceState.SelfMute
		payload["self_deaf"] = event.VoiceState.SelfDeaf
		payload["mute"] = event.VoiceState.GuildMute
		payload["deaf"] = event.VoiceState.GuildDeaf
	}
	daemon.emit(payload)
}

func (daemon *Daemon) onSlashCommand(event *events.ApplicationCommandInteractionCreate) {
	data, ok := event.Data.(disgodiscord.SlashCommandInteractionData)
	if !ok {
		return
	}
	_ = event.DeferCreateMessage(false)

	tokenKey := uuid.NewString()
	daemon.mutex.Lock()
	daemon.interactions[tokenKey] = &storedInteraction{
		id:            event.ID(),
		applicationID: event.ApplicationID(),
		token:         event.Token(),
		deferred:      true,
		responded:     true,
	}
	daemon.mutex.Unlock()

	args := make(map[string]string, len(data.Options))
	for name, option := range data.Options {
		var strVal string
		if json.Unmarshal(option.Value, &strVal) == nil {
			args[name] = strVal
		} else {
			args[name] = string(option.Value)
		}
	}

	var guildID any
	if event.GuildID() != nil {
		guildID = event.GuildID().String()
	}
	isAdmin := false
	var memberPerms int64
	if event.Member() != nil {
		memberPerms = int64(event.Member().Permissions)
		isAdmin = event.Member().Permissions.Has(disgodiscord.PermissionAdministrator)
	}

	daemon.emit(map[string]any{
		"event":              "slash_command",
		"command":            data.CommandName(),
		"args":               args,
		"channel_id":         event.Channel().ID().String(),
		"user":               voxdiscord.FormatUserTag(event.User()),
		"user_id":            event.User().ID.String(),
		"guild_id":           guildID,
		"interaction_token":  tokenKey,
		"is_admin":           isAdmin,
		"member_permissions": memberPerms,
	})
}

func (daemon *Daemon) onComponentInteraction(event *events.ComponentInteractionCreate) {
	customID := event.Data.CustomID()
	var values []string
	if selectData, ok := event.Data.(disgodiscord.StringSelectMenuInteractionData); ok {
		values = selectData.Values
	}

	switch interact.RouteCustomID(customID) {
	case "workflow":
		reply, deferred, err := daemon.interact.HandleWorkflowInteraction(event.User().ID.String(), customID, values)
		if err != nil {
			daemon.options.Logger.Warn("workflow interaction error", "error", err)
		}
		if reply != "" {
			_ = event.CreateMessage(disgodiscord.MessageCreate{Content: reply, Flags: disgodiscord.MessageFlagEphemeral})
		} else if deferred {
			_ = event.DeferUpdateMessage()
		}
		return
	case "dashboard":
		reply, err := daemon.interact.HandleDashboardInteraction(event.User().ID.String(), customID)
		if err != nil {
			daemon.options.Logger.Warn("dashboard interaction error", "error", err)
		}
		if reply != "" {
			_ = event.CreateMessage(disgodiscord.MessageCreate{Content: reply, Flags: disgodiscord.MessageFlagEphemeral})
		} else {
			_ = event.DeferUpdateMessage()
		}
		return
	}

	tokenKey := uuid.NewString()
	autoCtx, autoCancel := context.WithCancel(context.Background())
	stored := &storedInteraction{
		id:            event.ID(),
		applicationID: event.ApplicationID(),
		token:         event.Token(),
		autoDeferStop: autoCancel,
	}
	daemon.mutex.Lock()
	daemon.interactions[tokenKey] = stored
	daemon.mutex.Unlock()

	// Defer automatically after 2.5s if the external agent hasn't responded yet.
	go func() {
		defer autoCancel()
		timer := time.NewTimer(ComponentAutoDeferDelay)
		defer timer.Stop()
		select {
		case <-autoCtx.Done():
			return
		case <-timer.C:
			stored.mutex.Lock()
			if !stored.responded {
				stored.responded = true
				stored.deferred = true
				_ = event.DeferUpdateMessage()
			}
			stored.mutex.Unlock()
		}
	}()

	var guildID any
	if event.GuildID() != nil {
		guildID = event.GuildID().String()
	}
	if values == nil {
		values = []string{}
	}
	daemon.emit(map[string]any{
		"event":             "component_interaction",
		"custom_id":         customID,
		"component_type":    int(event.Data.Type()),
		"values":            values,
		"channel_id":        event.Channel().ID().String(),
		"message_id":        event.Message.ID.String(),
		"user":              voxdiscord.FormatUserTag(event.User()),
		"user_id":           event.User().ID.String(),
		"guild_id":          guildID,
		"interaction_token": tokenKey,
	})
}

func (daemon *Daemon) onModalSubmit(event *events.ModalSubmitInteractionCreate) {
	_ = event.DeferCreateMessage(false)

	tokenKey := uuid.NewString()
	daemon.mutex.Lock()
	daemon.interactions[tokenKey] = &storedInteraction{
		id:            event.ID(),
		applicationID: event.ApplicationID(),
		token:         event.Token(),
		deferred:      true,
		responded:     true,
	}
	daemon.mutex.Unlock()

	fields := make(map[string]string)
	for component := range event.Data.AllComponents() {
		if textInput, ok := component.(disgodiscord.TextInputComponent); ok {
			fields[textInput.CustomID] = textInput.Value
		}
	}
	var guildID any
	if event.GuildID() != nil {
		guildID = event.GuildID().String()
	}
	daemon.emit(map[string]any{
		"event":             "modal_submit",
		"custom_id":         event.Data.CustomID,
		"fields":            fields,
		"channel_id":        event.Channel().ID().String(),
		"user":              voxdiscord.FormatUserTag(event.User()),
		"user_id":           event.User().ID.String(),
		"guild_id":          guildID,
		"interaction_token": tokenKey,
	})
}

func (daemon *Daemon) guildMeta(guildID *snowflake.ID) (any, any) {
	if guildID == nil {
		return nil, nil
	}
	if guild, ok := daemon.client.Caches.Guild(*guildID); ok {
		return guild.Name, guildID.String()
	}
	return nil, guildID.String()
}

func (daemon *Daemon) channelName(channelID snowflake.ID, isDM bool) string {
	if isDM {
		return "DM"
	}
	if ch, ok := daemon.client.Caches.Channel(channelID); ok {
		return ch.Name()
	}
	return channelID.String()
}
