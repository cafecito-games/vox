package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/interact"
)

// Dispatch executes one parsed JSONL action request and returns its response map.
func (daemon *Daemon) Dispatch(ctx context.Context, req RequestEnvelope) map[string]any {
	if req.Action == "" {
		return map[string]any{"error": "Missing 'action' field"}
	}
	result, err := daemon.dispatchAction(ctx, req)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return result
}

func (daemon *Daemon) dispatchAction(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	switch req.Action {
	// --- Messaging ---
	case "send":
		return daemon.actionSend(req, false)
	case "reply":
		return daemon.actionSend(req, true)
	case "edit":
		return daemon.actionEdit(req)
	case "delete":
		return daemon.actionDelete(req)
	case "message_list":
		return daemon.actionMessageList(req)
	case "message_get":
		return daemon.actionMessageGet(req)
	case "message_search":
		return daemon.actionMessageSearch(req)
	case "message_pin":
		return daemon.actionMessagePin(req, true)
	case "message_unpin":
		return daemon.actionMessagePin(req, false)
	case "message_bulk_delete":
		return daemon.actionMessageBulkDelete(req)

	// --- Streaming ---
	case "stream_start":
		return daemon.actionStreamStart(req)
	case "stream_chunk":
		return daemon.actionStreamChunk(req)
	case "stream_end":
		return daemon.actionStreamEnd(req)

	// --- Interactions ---
	case "interaction_followup":
		return daemon.actionInteractionFollowup(req)
	case "interaction_respond":
		return daemon.actionInteractionRespond(req)
	case "interaction_edit":
		return daemon.actionInteractionEdit(req)
	case "modal_send":
		return daemon.actionModalSend(req)

	// --- Typing / Presence ---
	case "typing_start":
		return daemon.actionTypingStart(req)
	case "typing_stop":
		return daemon.actionTypingStop(req)
	case "presence":
		return daemon.actionPresence(ctx, req)

	// --- Reactions ---
	case "reaction_add":
		return daemon.actionReaction(req, true)
	case "reaction_remove":
		return daemon.actionReaction(req, false)
	case "reaction_users":
		return daemon.actionReactionUsers(req)

	// --- Threads ---
	case "thread_create":
		return daemon.actionThreadCreate(req)
	case "thread_send":
		return daemon.actionThreadSend(req)
	case "thread_list":
		return daemon.actionThreadList(req)
	case "thread_archive":
		return daemon.actionThreadArchive(req)
	case "thread_rename":
		return daemon.actionThreadRename(req)
	case "thread_add_member":
		return daemon.actionThreadMember(req, true)
	case "thread_remove_member":
		return daemon.actionThreadMember(req, false)

	// --- Polls ---
	case "poll_send":
		return daemon.actionPollSend(req)
	case "poll_results":
		return daemon.actionPollResults(req)
	case "poll_end":
		return daemon.actionPollEnd(req)

	// --- Channels & Forums ---
	case "channel_list":
		return daemon.actionChannelList(req)
	case "channel_create":
		return daemon.actionChannelCreate(req)
	case "channel_info":
		return daemon.actionChannelInfo(req)
	case "channel_edit":
		return daemon.actionChannelEdit(req)
	case "channel_set_permissions":
		return daemon.actionChannelSetPermissions(req)
	case "forum_post":
		return daemon.actionForumPost(req)

	// --- DMs ---
	case "dm_send":
		return daemon.actionDMSend(req)

	// --- Members & Roles ---
	case "member_list":
		return daemon.actionMemberList(req)
	case "member_info":
		return daemon.actionMemberInfo(req)
	case "member_timeout":
		return daemon.actionMemberTimeout(req)
	case "role_list":
		return daemon.actionRoleList(req)
	case "role_assign":
		return daemon.actionRoleMembership(req, true)
	case "role_remove":
		return daemon.actionRoleMembership(req, false)
	case "role_edit":
		return daemon.actionRoleEdit(req)

	// --- Webhooks ---
	case "webhook_list":
		return daemon.actionWebhookList(req)
	case "webhook_create":
		return daemon.actionWebhookCreate(req)
	case "webhook_delete":
		return daemon.actionWebhookDelete(req)

	// --- Scheduled Events ---
	case "event_list":
		return daemon.actionEventList(req)
	case "event_create":
		return daemon.actionEventCreate(req)

	// --- Servers ---
	case "server_list":
		return daemon.actionServerList()
	case "server_info":
		return daemon.actionServerInfo(req)

	// --- Voice ---
	case "voice_connect":
		return daemon.actionVoiceConnect(ctx, req)
	case "voice_disconnect":
		return daemon.actionVoiceDisconnect(ctx, req)
	case "voice_move":
		return daemon.actionVoiceMove(ctx, req)
	case "voice_speak":
		return daemon.actionVoiceSpeak(ctx, req)
	case "voice_play":
		return daemon.actionVoicePlay(req)
	case "voice_stop":
		return daemon.actionVoiceControl(req, "stop")
	case "voice_pause":
		return daemon.actionVoiceControl(req, "pause")
	case "voice_resume":
		return daemon.actionVoiceControl(req, "resume")
	case "voice_listen_start":
		return daemon.actionVoiceListenStart(req)
	case "voice_listen_stop":
		return daemon.actionVoiceListenStop(req)
	case "voice_status":
		return map[string]any{"ok": true, "connections": daemon.voice.Status()}, nil
	case "voice_where":
		return daemon.actionVoiceWhere(req)
	case "voice_members":
		return daemon.actionVoiceMembers(req)
	case "voice_set_config":
		return daemon.actionVoiceSetConfig(req)

	// --- Workflows & Dashboards ---
	case "workflow_start":
		return daemon.actionWorkflowStart(req)
	case "workflow_cancel":
		return daemon.actionWorkflowCancel(req)
	case "dashboard_create":
		return daemon.actionDashboardCreate(req)
	case "dashboard_update":
		return daemon.actionDashboardUpdate(req)
	case "dashboard_delete":
		return daemon.actionDashboardDelete(req)

	default:
		return nil, fmt.Errorf("unknown action: %s", req.Action)
	}
}

func (daemon *Daemon) actionSend(req RequestEnvelope, isReply bool) (map[string]any, error) {
	var body struct {
		ChannelID  string                `json:"channel_id"`
		MessageID  string                `json:"message_id"`
		Content    string                `json:"content"`
		Files      []string              `json:"files"`
		Embed      *voxdiscord.EmbedSpec `json:"embed"`
		Components json.RawMessage       `json:"components"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	create := disgodiscord.MessageCreate{Content: body.Content}
	if isReply {
		msgID, err := parseRequiredID("message_id", body.MessageID)
		if err != nil {
			return nil, err
		}
		create.MessageReference = &disgodiscord.MessageReference{MessageID: &msgID}
	}
	if body.Embed != nil && !body.Embed.IsEmpty() {
		embed, err := voxdiscord.BuildEmbed(*body.Embed)
		if err != nil {
			return nil, err
		}
		create.Embeds = []disgodiscord.Embed{embed}
	}
	if len(body.Components) > 0 {
		comps, err := voxdiscord.BuildComponentsFromRaw(body.Components)
		if err != nil {
			return nil, err
		}
		create.Components = comps
	}
	files, cleanup, err := voxdiscord.OpenFiles(body.Files)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	create.Files = files

	msg, err := daemon.client.Rest.CreateMessage(channelID, create)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{
		"ok":         true,
		"message_id": msg.ID.String(),
		"jump_url":   msg.JumpURL(),
	}, nil
}

func (daemon *Daemon) actionEdit(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	content := strField(req.Raw, "content")
	if _, err := daemon.client.Rest.UpdateMessage(channelID, msgID, disgodiscord.MessageUpdate{Content: &content}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionDelete(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	if err := daemon.client.Rest.DeleteMessage(channelID, msgID); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionMessageList(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	limit := intField(req.Raw, "limit", 20)
	messages, err := daemon.client.Rest.GetMessages(channelID, 0, 0, 0, limit)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	items := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		atts := make([]map[string]any, 0, len(m.Attachments))
		for _, att := range m.Attachments {
			atts = append(atts, map[string]any{"filename": att.Filename, "url": att.URL})
		}
		items = append(items, map[string]any{
			"id":          m.ID.String(),
			"author":      voxdiscord.FormatUserTag(m.Author),
			"author_id":   m.Author.ID.String(),
			"content":     m.Content,
			"timestamp":   m.CreatedAt.UTC().Format(time.RFC3339),
			"attachments": atts,
		})
	}
	return map[string]any{"ok": true, "messages": items}, nil
}

func (daemon *Daemon) actionMessageGet(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	m, err := daemon.client.Rest.GetMessage(channelID, msgID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	atts := make([]map[string]any, 0, len(m.Attachments))
	for _, att := range m.Attachments {
		atts = append(atts, map[string]any{"filename": att.Filename, "url": att.URL})
	}
	embeds := make([]map[string]any, 0, len(m.Embeds))
	for _, e := range m.Embeds {
		embeds = append(embeds, map[string]any{"title": e.Title, "description": e.Description})
	}
	var replyTo any
	if m.MessageReference != nil && m.MessageReference.MessageID != nil {
		replyTo = m.MessageReference.MessageID.String()
	}
	return map[string]any{
		"ok":          true,
		"id":          m.ID.String(),
		"author":      voxdiscord.FormatUserTag(m.Author),
		"author_id":   m.Author.ID.String(),
		"content":     m.Content,
		"timestamp":   m.CreatedAt.UTC().Format(time.RFC3339),
		"attachments": atts,
		"embeds":      embeds,
		"reply_to":    replyTo,
		"jump_url":    m.JumpURL(),
	}, nil
}

func (daemon *Daemon) actionMessageSearch(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strField(req.Raw, "query"))
	authorFilter := strings.ToLower(strField(req.Raw, "author"))
	limit := min(intField(req.Raw, "limit", 100), 100)
	messages, err := daemon.client.Rest.GetMessages(channelID, 0, 0, 0, limit)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	matches := make([]map[string]any, 0)
	for _, m := range messages {
		authorTag := voxdiscord.FormatUserTag(m.Author)
		if authorFilter != "" && !strings.Contains(strings.ToLower(authorTag), authorFilter) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(m.Content), query) {
			continue
		}
		matches = append(matches, map[string]any{
			"id":        m.ID.String(),
			"author":    authorTag,
			"content":   m.Content,
			"timestamp": m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{"ok": true, "messages": matches}, nil
}

func (daemon *Daemon) actionMessagePin(req RequestEnvelope, pin bool) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	if pin {
		err = daemon.client.Rest.PinMessage(channelID, msgID)
	} else {
		err = daemon.client.Rest.UnpinMessage(channelID, msgID)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionMessageBulkDelete(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID  string   `json:"channel_id"`
		MessageIDs []string `json:"message_ids"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	if len(body.MessageIDs) == 0 {
		return nil, errors.New("message_ids cannot be empty")
	}
	ids := make([]snowflake.ID, 0, len(body.MessageIDs))
	for _, raw := range body.MessageIDs {
		id, err := parseRequiredID("message_id", raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 1 {
		err = daemon.client.Rest.DeleteMessage(channelID, ids[0])
	} else {
		err = daemon.client.Rest.BulkDeleteMessages(channelID, ids)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "deleted": len(ids)}, nil
}

// --- Streaming ---

func (daemon *Daemon) actionStreamStart(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	replyTo := strField(req.Raw, "reply_to")
	interactionToken := strField(req.Raw, "interaction_token")

	daemon.stopTyping(channelID)

	var msg *disgodiscord.Message
	if interactionToken != "" {
		inter, ok := daemon.getInteraction(interactionToken, true)
		if ok {
			msg, err = daemon.client.Rest.CreateFollowupMessage(inter.applicationID, inter.token, disgodiscord.MessageCreate{Content: "..."})
		}
	}
	if msg == nil && err == nil {
		create := disgodiscord.MessageCreate{Content: "..."}
		if replyTo != "" {
			if refID, ok := voxdiscord.ParseSnowflake(replyTo); ok {
				create.MessageReference = &disgodiscord.MessageReference{MessageID: &refID}
			}
		}
		msg, err = daemon.client.Rest.CreateMessage(channelID, create)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}

	streamID := uuid.NewString()[:8]
	flushCtx, flushCancel := context.WithCancel(context.Background())
	stream := &streamState{
		id:          streamID,
		channelID:   channelID,
		messageID:   msg.ID,
		cancelFlush: flushCancel,
	}

	daemon.mutex.Lock()
	daemon.streams[streamID] = stream
	daemon.mutex.Unlock()

	go func() {
		defer flushCancel()
		daemon.runStreamFlushLoop(flushCtx, stream)
	}()

	return map[string]any{
		"ok":         true,
		"stream_id":  streamID,
		"message_id": msg.ID.String(),
	}, nil
}

func (daemon *Daemon) runStreamFlushLoop(ctx context.Context, stream *streamState) {
	ticker := time.NewTicker(StreamFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stream.mutex.Lock()
			if stream.done {
				stream.mutex.Unlock()
				return
			}
			content := stream.buffer.String()
			if content == stream.lastFlushed || content == "" {
				stream.mutex.Unlock()
				continue
			}
			display := content
			if runes := []rune(display); len(runes) > DiscordMessageLimit {
				display = string(runes[:DiscordMessageLimit])
			}
			stream.lastFlushed = content
			stream.mutex.Unlock()

			if _, err := daemon.client.Rest.UpdateMessage(stream.channelID, stream.messageID, disgodiscord.MessageUpdate{Content: &display}); err != nil {
				daemon.emit(map[string]any{
					"event":     "error",
					"stream_id": stream.id,
					"message":   fmt.Sprintf("stream flush failed: %v", err),
				})
			}
		}
	}
}

func (daemon *Daemon) actionStreamChunk(req RequestEnvelope) (map[string]any, error) {
	streamID := strField(req.Raw, "stream_id")
	daemon.mutex.Lock()
	stream, ok := daemon.streams[streamID]
	daemon.mutex.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown stream_id: %s", streamID)
	}
	stream.mutex.Lock()
	stream.buffer.WriteString(strField(req.Raw, "content"))
	stream.mutex.Unlock()
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionStreamEnd(req RequestEnvelope) (map[string]any, error) {
	streamID := strField(req.Raw, "stream_id")
	daemon.mutex.Lock()
	stream, ok := daemon.streams[streamID]
	delete(daemon.streams, streamID)
	daemon.mutex.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown stream_id: %s", streamID)
	}

	stream.mutex.Lock()
	stream.done = true
	if stream.cancelFlush != nil {
		stream.cancelFlush()
	}
	fullContent := stream.buffer.String()
	stream.mutex.Unlock()

	chunks := SplitMessageChunks(fullContent, DiscordMessageLimit)
	first := chunks[0]
	if _, err := daemon.client.Rest.UpdateMessage(stream.channelID, stream.messageID, disgodiscord.MessageUpdate{Content: &first}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	for _, extra := range chunks[1:] {
		if _, sendErr := daemon.client.Rest.CreateMessage(stream.channelID, disgodiscord.MessageCreate{Content: extra}); sendErr != nil {
			daemon.options.Logger.Warn("failed to send stream overflow chunk", "stream_id", streamID, "error", sendErr)
			break
		}
	}
	return map[string]any{"ok": true, "message_id": stream.messageID.String()}, nil
}

// --- Interactions ---

func (daemon *Daemon) getInteraction(tokenKey string, pop bool) (*storedInteraction, bool) {
	daemon.mutex.Lock()
	defer daemon.mutex.Unlock()
	inter, ok := daemon.interactions[tokenKey]
	if ok && pop {
		delete(daemon.interactions, tokenKey)
		if inter.autoDeferStop != nil {
			inter.autoDeferStop()
		}
	}
	return inter, ok
}

func (daemon *Daemon) actionInteractionFollowup(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		InteractionToken string                `json:"interaction_token"`
		Content          string                `json:"content"`
		Embed            *voxdiscord.EmbedSpec `json:"embed"`
		Components       json.RawMessage       `json:"components"`
		Ephemeral        bool                  `json:"ephemeral"`
		KeepToken        bool                  `json:"keep_token"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	inter, ok := daemon.getInteraction(body.InteractionToken, !body.KeepToken)
	if !ok {
		return nil, fmt.Errorf("unknown interaction_token: %s", body.InteractionToken)
	}
	inter.mutex.Lock()
	if !inter.responded {
		_ = daemon.client.Rest.CreateInteractionResponse(inter.id, inter.token, disgodiscord.InteractionResponse{
			Type: disgodiscord.InteractionResponseTypeDeferredCreateMessage,
		})
		inter.responded = true
		inter.deferred = true
	}
	inter.mutex.Unlock()

	create := disgodiscord.MessageCreate{Content: body.Content}
	if body.Ephemeral {
		create.Flags = disgodiscord.MessageFlagEphemeral
	}
	if body.Embed != nil && !body.Embed.IsEmpty() {
		embed, err := voxdiscord.BuildEmbed(*body.Embed)
		if err != nil {
			return nil, err
		}
		create.Embeds = []disgodiscord.Embed{embed}
	}
	if len(body.Components) > 0 {
		comps, err := voxdiscord.BuildComponentsFromRaw(body.Components)
		if err != nil {
			return nil, err
		}
		create.Components = comps
	}
	msg, err := daemon.client.Rest.CreateFollowupMessage(inter.applicationID, inter.token, create)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "message_id": msg.ID.String()}, nil
}

func (daemon *Daemon) actionInteractionRespond(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		InteractionToken string                `json:"interaction_token"`
		Content          string                `json:"content"`
		Embed            *voxdiscord.EmbedSpec `json:"embed"`
		Components       json.RawMessage       `json:"components"`
		Ephemeral        bool                  `json:"ephemeral"`
		KeepToken        bool                  `json:"keep_token"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	inter, ok := daemon.getInteraction(body.InteractionToken, !body.KeepToken)
	if !ok {
		return nil, fmt.Errorf("unknown interaction_token: %s", body.InteractionToken)
	}
	inter.mutex.Lock()
	defer inter.mutex.Unlock()
	if inter.responded {
		return nil, errors.New("interaction already responded to; use interaction_followup or interaction_edit")
	}

	create := disgodiscord.MessageCreate{Content: body.Content}
	if body.Ephemeral {
		create.Flags = disgodiscord.MessageFlagEphemeral
	}
	if body.Embed != nil && !body.Embed.IsEmpty() {
		embed, err := voxdiscord.BuildEmbed(*body.Embed)
		if err != nil {
			return nil, err
		}
		create.Embeds = []disgodiscord.Embed{embed}
	}
	if len(body.Components) > 0 {
		comps, err := voxdiscord.BuildComponentsFromRaw(body.Components)
		if err != nil {
			return nil, err
		}
		create.Components = comps
	}
	if err := daemon.client.Rest.CreateInteractionResponse(inter.id, inter.token, disgodiscord.InteractionResponse{
		Type: disgodiscord.InteractionResponseTypeCreateMessage,
		Data: create,
	}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	inter.responded = true
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionInteractionEdit(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		InteractionToken string                `json:"interaction_token"`
		Content          *string               `json:"content"`
		Embed            *voxdiscord.EmbedSpec `json:"embed"`
		Components       json.RawMessage       `json:"components"`
		KeepToken        bool                  `json:"keep_token"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	inter, ok := daemon.getInteraction(body.InteractionToken, !body.KeepToken)
	if !ok {
		return nil, fmt.Errorf("unknown interaction_token: %s", body.InteractionToken)
	}
	update := disgodiscord.MessageUpdate{Content: body.Content}
	if body.Embed != nil && !body.Embed.IsEmpty() {
		embed, err := voxdiscord.BuildEmbed(*body.Embed)
		if err != nil {
			return nil, err
		}
		embeds := []disgodiscord.Embed{embed}
		update.Embeds = &embeds
	}
	if len(body.Components) > 0 {
		comps, err := voxdiscord.BuildComponentsFromRaw(body.Components)
		if err != nil {
			return nil, err
		}
		update.Components = &comps
	}
	if _, err := daemon.client.Rest.UpdateInteractionResponse(inter.applicationID, inter.token, update); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionModalSend(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		InteractionToken string                    `json:"interaction_token"`
		Title            string                    `json:"title"`
		CustomID         string                    `json:"custom_id"`
		Fields           []interact.ModalFieldSpec `json:"fields"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	inter, ok := daemon.getInteraction(body.InteractionToken, true)
	if !ok {
		return nil, fmt.Errorf("unknown interaction_token: %s", body.InteractionToken)
	}
	modal, err := interact.BuildModal(body.Title, body.CustomID, body.Fields)
	if err != nil {
		return nil, err
	}
	inter.mutex.Lock()
	defer inter.mutex.Unlock()
	if err := daemon.client.Rest.CreateInteractionResponse(inter.id, inter.token, disgodiscord.InteractionResponse{
		Type: disgodiscord.InteractionResponseTypeModal,
		Data: modal,
	}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	inter.responded = true
	return map[string]any{"ok": true}, nil
}

// --- Typing & Presence ---

func (daemon *Daemon) actionTypingStart(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	daemon.stopTyping(channelID)
	ctx, cancel := context.WithCancel(context.Background())
	daemon.mutex.Lock()
	daemon.typingTasks[channelID] = cancel
	daemon.mutex.Unlock()

	go func() {
		defer cancel()
		_ = daemon.client.Rest.SendTyping(channelID)
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = daemon.client.Rest.SendTyping(channelID)
			}
		}
	}()
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) stopTyping(channelID snowflake.ID) {
	daemon.mutex.Lock()
	cancel, ok := daemon.typingTasks[channelID]
	delete(daemon.typingTasks, channelID)
	daemon.mutex.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (daemon *Daemon) actionTypingStop(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	daemon.stopTyping(channelID)
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionPresence(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	status := strField(req.Raw, "status")
	if status == "" {
		status = "online"
	}
	if err := daemon.setPresence(ctx, status, strField(req.Raw, "activity_type"), strField(req.Raw, "activity_text")); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// --- Reactions ---

func (daemon *Daemon) actionReaction(req RequestEnvelope, add bool) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	emoji := strField(req.Raw, "emoji")
	if emoji == "" {
		return nil, errors.New("missing emoji")
	}
	if add {
		err = daemon.client.Rest.AddReaction(channelID, msgID, emoji)
	} else {
		err = daemon.client.Rest.RemoveOwnReaction(channelID, msgID, emoji)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionReactionUsers(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	emoji := strField(req.Raw, "emoji")
	limit := intField(req.Raw, "limit", 100)
	users, err := daemon.client.Rest.GetReactions(channelID, msgID, emoji, disgodiscord.MessageReactionTypeNormal, 0, limit)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id":   u.ID.String(),
			"name": voxdiscord.FormatUserTag(u),
			"bot":  u.Bot,
		})
	}
	return map[string]any{"ok": true, "users": out}, nil
}

// --- Threads ---

func (daemon *Daemon) actionThreadCreate(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	name := strField(req.Raw, "name")
	if name == "" {
		return nil, errors.New("missing thread name")
	}
	autoArchive := disgodiscord.AutoArchiveDuration(intField(req.Raw, "auto_archive_duration", 1440))
	var thread *disgodiscord.GuildThread
	if msgRaw := strField(req.Raw, "message_id"); msgRaw != "" {
		msgID, err := parseRequiredID("message_id", msgRaw)
		if err != nil {
			return nil, err
		}
		thread, err = daemon.client.Rest.CreateThreadFromMessage(channelID, msgID, disgodiscord.ThreadCreateFromMessage{
			Name:                name,
			AutoArchiveDuration: autoArchive,
		})
		if err != nil {
			return nil, voxdiscord.MapError(err)
		}
	} else {
		thread, err = daemon.client.Rest.CreateThread(channelID, disgodiscord.GuildPublicThreadCreate{
			Name:                name,
			AutoArchiveDuration: autoArchive,
		})
		if err != nil {
			return nil, voxdiscord.MapError(err)
		}
	}
	if initialContent := strField(req.Raw, "content"); initialContent != "" {
		_, _ = daemon.client.Rest.CreateMessage(thread.ID(), disgodiscord.MessageCreate{Content: initialContent})
	}
	return map[string]any{
		"ok":          true,
		"thread_id":   thread.ID().String(),
		"thread_name": thread.Name(),
	}, nil
}

func (daemon *Daemon) actionThreadSend(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ThreadID string   `json:"thread_id"`
		Content  string   `json:"content"`
		Files    []string `json:"files"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	threadID, err := parseRequiredID("thread_id", body.ThreadID)
	if err != nil {
		return nil, err
	}
	files, cleanup, err := voxdiscord.OpenFiles(body.Files)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	msg, err := daemon.client.Rest.CreateMessage(threadID, disgodiscord.MessageCreate{
		Content: body.Content,
		Files:   files,
	})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "message_id": msg.ID.String()}, nil
}

func (daemon *Daemon) actionThreadList(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	ch, err := daemon.client.Rest.GetChannel(channelID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	guildCh, ok := ch.(disgodiscord.GuildChannel)
	if !ok {
		return nil, errors.New("channel is not a guild channel")
	}
	active, err := daemon.client.Rest.GetActiveGuildThreads(guildCh.GuildID())
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	out := make([]map[string]any, 0)
	for _, t := range active.Threads {
		if t.ParentID() == nil || *t.ParentID() != channelID {
			continue
		}
		out = append(out, map[string]any{
			"id":            t.ID().String(),
			"name":          t.Name(),
			"message_count": t.MessageCount,
			"member_count":  t.MemberCount,
			"archived":      t.ThreadMetadata.Archived,
		})
	}
	return map[string]any{"ok": true, "threads": out}, nil
}

func (daemon *Daemon) actionThreadArchive(req RequestEnvelope) (map[string]any, error) {
	threadID, err := parseRequiredID("thread_id", strField(req.Raw, "thread_id"))
	if err != nil {
		return nil, err
	}
	archived := boolField(req.Raw, "archived", true)
	if _, err := daemon.client.Rest.UpdateChannel(threadID, disgodiscord.GuildThreadUpdate{Archived: &archived}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "thread_id": threadID.String()}, nil
}

func (daemon *Daemon) actionThreadRename(req RequestEnvelope) (map[string]any, error) {
	threadID, err := parseRequiredID("thread_id", strField(req.Raw, "thread_id"))
	if err != nil {
		return nil, err
	}
	name := strField(req.Raw, "name")
	if name == "" {
		return nil, errors.New("missing name")
	}
	if _, err := daemon.client.Rest.UpdateChannel(threadID, disgodiscord.GuildThreadUpdate{Name: &name}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "thread_id": threadID.String(), "name": name}, nil
}

func (daemon *Daemon) actionThreadMember(req RequestEnvelope, add bool) (map[string]any, error) {
	threadID, err := parseRequiredID("thread_id", strField(req.Raw, "thread_id"))
	if err != nil {
		return nil, err
	}
	memberID, err := parseRequiredID("member_id", strField(req.Raw, "member_id"))
	if err != nil {
		return nil, err
	}
	if add {
		err = daemon.client.Rest.AddThreadMember(threadID, memberID)
	} else {
		err = daemon.client.Rest.RemoveThreadMember(threadID, memberID)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

// --- Polls ---

func (daemon *Daemon) actionPollSend(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID     string `json:"channel_id"`
		Question      string `json:"question"`
		Answers       []any  `json:"answers"`
		DurationHours int    `json:"duration_hours"`
		Multiple      bool   `json:"multiple"`
		Content       string `json:"content"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	if len(body.Answers) < 2 {
		return nil, errors.New("poll requires at least 2 answers")
	}
	if body.DurationHours <= 0 {
		body.DurationHours = 24
	}
	answers := make([]disgodiscord.PollMedia, 0, len(body.Answers))
	for _, rawAnswer := range body.Answers {
		switch ans := rawAnswer.(type) {
		case string:
			text := ans
			answers = append(answers, disgodiscord.PollMedia{Text: &text})
		case map[string]any:
			text, _ := ans["text"].(string)
			media := disgodiscord.PollMedia{Text: &text}
			if emojiStr, ok := ans["emoji"].(string); ok && emojiStr != "" {
				media.Emoji = &disgodiscord.PartialEmoji{Name: &emojiStr}
			}
			answers = append(answers, media)
		}
	}
	q := body.Question
	msg, err := daemon.client.Rest.CreateMessage(channelID, disgodiscord.MessageCreate{
		Content: body.Content,
		Poll: &disgodiscord.PollCreate{
			Question:         disgodiscord.PollMedia{Text: &q},
			Answers:          answers,
			Duration:         body.DurationHours,
			AllowMultiselect: body.Multiple,
		},
	})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "message_id": msg.ID.String()}, nil
}

func (daemon *Daemon) actionPollResults(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	msg, err := daemon.client.Rest.GetMessage(channelID, msgID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	if msg.Poll == nil {
		return nil, errors.New("message has no poll")
	}
	countsByID := make(map[int]int)
	totalVotes := 0
	isFinalized := false
	if msg.Poll.Results != nil {
		isFinalized = msg.Poll.Results.IsFinalized
		for _, ac := range msg.Poll.Results.AnswerCounts {
			countsByID[ac.ID] = ac.Count
			totalVotes += ac.Count
		}
	}
	answers := make([]map[string]any, 0, len(msg.Poll.Answers))
	for _, ans := range msg.Poll.Answers {
		text := ""
		if ans.PollMedia.Text != nil {
			text = *ans.PollMedia.Text
		}
		answerID := 0
		if ans.AnswerID != nil {
			answerID = *ans.AnswerID
		}
		answers = append(answers, map[string]any{
			"id":         answerID,
			"text":       text,
			"vote_count": countsByID[answerID],
		})
	}
	question := ""
	if msg.Poll.Question.Text != nil {
		question = *msg.Poll.Question.Text
	}
	return map[string]any{
		"ok":           true,
		"question":     question,
		"total_votes":  totalVotes,
		"is_finalised": isFinalized,
		"answers":      answers,
	}, nil
}

func (daemon *Daemon) actionPollEnd(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	msgID, err := parseRequiredID("message_id", strField(req.Raw, "message_id"))
	if err != nil {
		return nil, err
	}
	if _, err := daemon.client.Rest.ExpirePoll(channelID, msgID); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

// --- Channels & Forums ---

func (daemon *Daemon) actionChannelList(req RequestEnvelope) (map[string]any, error) {
	guildIDRaw := strField(req.Raw, "guild_id")
	var guildIDs []snowflake.ID
	if guildIDRaw != "" {
		gid, err := parseRequiredID("guild_id", guildIDRaw)
		if err != nil {
			return nil, err
		}
		guildIDs = []snowflake.ID{gid}
	} else {
		guilds, err := voxdiscord.FetchAllGuilds(daemon.client.Rest)
		if err != nil {
			return nil, err
		}
		for _, g := range guilds {
			guildIDs = append(guildIDs, g.ID)
		}
	}

	out := make([]map[string]any, 0)
	for _, gid := range guildIDs {
		gName, _ := daemon.guildMeta(&gid)
		channels, err := daemon.client.Rest.GetGuildChannels(gid)
		if err != nil {
			continue
		}
		for _, ch := range channels {
			var kind string
			switch ch.Type() {
			case disgodiscord.ChannelTypeGuildText:
				kind = "text"
			case disgodiscord.ChannelTypeGuildVoice:
				kind = "voice"
			case disgodiscord.ChannelTypeGuildForum:
				kind = "forum"
			default:
				continue
			}
			out = append(out, map[string]any{
				"id":        ch.ID().String(),
				"name":      ch.Name(),
				"type":      kind,
				"server":    gName,
				"server_id": gid.String(),
			})
		}
	}
	return map[string]any{"ok": true, "channels": out, "visible_only": true}, nil
}

func (daemon *Daemon) actionChannelCreate(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	name := strField(req.Raw, "name")
	if name == "" {
		return nil, errors.New("missing name")
	}
	kind := strings.ToLower(strField(req.Raw, "type"))
	if kind == "" {
		kind = "text"
	}
	var payload disgodiscord.GuildChannelCreate
	switch kind {
	case "text":
		payload = disgodiscord.GuildTextChannelCreate{Name: name}
	case "voice":
		payload = disgodiscord.GuildVoiceChannelCreate{Name: name}
	case "category":
		payload = disgodiscord.GuildCategoryChannelCreate{Name: name}
	case "forum":
		payload = disgodiscord.GuildForumChannelCreate{Name: name}
	default:
		return nil, fmt.Errorf("unsupported channel type: %s", kind)
	}
	ch, err := daemon.client.Rest.CreateGuildChannel(guildID, payload)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "channel_id": ch.ID().String(), "name": ch.Name()}, nil
}

func (daemon *Daemon) actionChannelInfo(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	ch, err := daemon.client.Rest.GetChannel(channelID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	var serverName, serverID, topic any
	if gch, ok := ch.(disgodiscord.GuildChannel); ok {
		gid := gch.GuildID()
		serverName, serverID = daemon.guildMeta(&gid)
	}
	if tch, ok := ch.(disgodiscord.GuildTextChannel); ok && tch.Topic() != nil {
		topic = *tch.Topic()
	}
	return map[string]any{
		"ok":         true,
		"id":         ch.ID().String(),
		"name":       ch.Name(),
		"type":       fmt.Sprintf("%d", ch.Type()),
		"server":     serverName,
		"server_id":  serverID,
		"topic":      topic,
		"created_at": ch.ID().Time().UTC().Format(time.RFC3339),
	}, nil
}

func (daemon *Daemon) actionChannelEdit(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID string  `json:"channel_id"`
		Name      *string `json:"name"`
		Topic     *string `json:"topic"`
		Slowmode  *int    `json:"slowmode"`
		NSFW      *bool   `json:"nsfw"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	var updated []string
	if body.Name != nil {
		updated = append(updated, "name")
	}
	if body.Topic != nil {
		updated = append(updated, "topic")
	}
	if body.Slowmode != nil {
		updated = append(updated, "slowmode_delay")
	}
	if body.NSFW != nil {
		updated = append(updated, "nsfw")
	}
	if len(updated) == 0 {
		return nil, errors.New("no channel changes specified")
	}
	if _, err := daemon.client.Rest.UpdateChannel(channelID, disgodiscord.GuildTextChannelUpdate{
		Name:             body.Name,
		Topic:            body.Topic,
		RateLimitPerUser: body.Slowmode,
		NSFW:             body.NSFW,
	}); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "channel_id": channelID.String(), "updated": updated}, nil
}

func (daemon *Daemon) actionChannelSetPermissions(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	targetID, err := parseRequiredID("target_id", strField(req.Raw, "target_id"))
	if err != nil {
		return nil, err
	}
	targetType := strings.ToLower(strField(req.Raw, "target_type"))
	if targetType == "" {
		targetType = "role"
	}
	allowList := parseStringListField(req.Raw["allow"])
	denyList := parseStringListField(req.Raw["deny"])
	allowMask, err := voxdiscord.ParsePermissionNames(allowList)
	if err != nil {
		return nil, err
	}
	denyMask, err := voxdiscord.ParsePermissionNames(denyList)
	if err != nil {
		return nil, err
	}
	var update disgodiscord.PermissionOverwriteUpdate
	if targetType == "member" {
		update = disgodiscord.MemberPermissionOverwriteUpdate{Allow: &allowMask, Deny: &denyMask}
	} else {
		update = disgodiscord.RolePermissionOverwriteUpdate{Allow: &allowMask, Deny: &denyMask}
	}
	if err := daemon.client.Rest.UpdatePermissionOverwrite(channelID, targetID, update); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionForumPost(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID string                `json:"channel_id"`
		Title     string                `json:"title"`
		Content   string                `json:"content"`
		Files     []string              `json:"files"`
		Embed     *voxdiscord.EmbedSpec `json:"embed"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	msgCreate := disgodiscord.MessageCreate{Content: body.Content}
	if body.Embed != nil && !body.Embed.IsEmpty() {
		embed, err := voxdiscord.BuildEmbed(*body.Embed)
		if err != nil {
			return nil, err
		}
		msgCreate.Embeds = []disgodiscord.Embed{embed}
	}
	files, cleanup, err := voxdiscord.OpenFiles(body.Files)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	msgCreate.Files = files

	post, err := daemon.client.Rest.CreatePostInThreadChannel(channelID, disgodiscord.ThreadChannelPostCreate{
		Name:    body.Title,
		Message: msgCreate,
	})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{
		"ok":          true,
		"thread_id":   post.ID().String(),
		"thread_name": post.Name(),
		"message_id":  post.Message.ID.String(),
	}, nil
}

// --- DMs ---

func (daemon *Daemon) actionDMSend(req RequestEnvelope) (map[string]any, error) {
	userID, err := parseRequiredID("user_id", strField(req.Raw, "user_id"))
	if err != nil {
		return nil, err
	}
	dm, err := daemon.client.Rest.CreateDMChannel(userID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	msg, err := daemon.client.Rest.CreateMessage(dm.ID(), disgodiscord.MessageCreate{Content: strField(req.Raw, "content")})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	recipient := userID.String()
	if len(dm.Recipients) > 0 {
		recipient = voxdiscord.FormatUserTag(dm.Recipients[0])
	}
	return map[string]any{"ok": true, "message_id": msg.ID.String(), "recipient": recipient}, nil
}

// --- Members & Roles ---

func (daemon *Daemon) actionMemberList(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	limit := intField(req.Raw, "limit", 50)
	members, err := voxdiscord.FetchGuildMembers(daemon.client.Rest, guildID, limit)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"id":   m.User.ID.String(),
			"name": voxdiscord.FormatUserTag(m.User),
			"nick": m.Nick,
			"bot":  m.User.Bot,
		})
	}
	return map[string]any{"ok": true, "members": out}, nil
}

func (daemon *Daemon) actionMemberInfo(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	memberID, err := parseRequiredID("member_id", strField(req.Raw, "member_id"))
	if err != nil {
		return nil, err
	}
	m, err := daemon.client.Rest.GetMember(guildID, memberID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	rolesByID := make(map[snowflake.ID]string)
	if roles, err := daemon.client.Rest.GetRoles(guildID); err == nil {
		for _, r := range roles {
			rolesByID[r.ID] = r.Name
		}
	}
	roleList := make([]map[string]any, 0, len(m.RoleIDs))
	for _, rid := range m.RoleIDs {
		if rid == guildID {
			continue
		}
		roleList = append(roleList, map[string]any{"id": rid.String(), "name": rolesByID[rid]})
	}
	var joinedAt any
	if m.JoinedAt != nil {
		joinedAt = m.JoinedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"ok":        true,
		"id":        m.User.ID.String(),
		"name":      voxdiscord.FormatUserTag(m.User),
		"nick":      m.Nick,
		"bot":       m.User.Bot,
		"roles":     roleList,
		"joined_at": joinedAt,
	}, nil
}

func (daemon *Daemon) actionMemberTimeout(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	memberID, err := parseRequiredID("member_id", strField(req.Raw, "member_id"))
	if err != nil {
		return nil, err
	}
	duration := intField(req.Raw, "duration", 0)
	if duration < 0 || duration > 2419200 {
		return nil, errors.New("duration must be between 0 and 2419200 seconds")
	}
	var until *time.Time
	if duration > 0 {
		t := time.Now().UTC().Add(time.Duration(duration) * time.Second)
		until = &t
	}
	updated, err := daemon.client.Rest.UpdateMember(guildID, memberID, disgodiscord.MemberUpdate{
		CommunicationDisabledUntil: omit.New(until),
	})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{
		"ok":       true,
		"member":   voxdiscord.FormatUserTag(updated.User),
		"duration": duration,
	}, nil
}

func (daemon *Daemon) actionRoleList(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	roles, err := daemon.client.Rest.GetRoles(guildID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	var counts map[snowflake.ID]int
	if boolField(req.Raw, "with_member_counts", false) {
		if fetched, err := daemon.client.Rest.GetRoleMemberCounts(guildID); err == nil {
			counts = fetched
		}
	}
	out := make([]map[string]any, 0, len(roles))
	for _, r := range roles {
		if r.ID == guildID || r.Name == "@everyone" {
			continue
		}
		var memberCount any
		if counts != nil {
			memberCount = counts[r.ID]
		}
		out = append(out, map[string]any{
			"id":      r.ID.String(),
			"name":    r.Name,
			"color":   fmt.Sprintf("#%06x", r.Color),
			"members": memberCount,
		})
	}
	return map[string]any{"ok": true, "roles": out}, nil
}

func (daemon *Daemon) actionRoleMembership(req RequestEnvelope, assign bool) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	memberID, err := parseRequiredID("member_id", strField(req.Raw, "member_id"))
	if err != nil {
		return nil, err
	}
	roleID, err := parseRequiredID("role_id", strField(req.Raw, "role_id"))
	if err != nil {
		return nil, err
	}
	if assign {
		err = daemon.client.Rest.AddMemberRole(guildID, memberID, roleID)
	} else {
		err = daemon.client.Rest.RemoveMemberRole(guildID, memberID, roleID)
	}
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "member": memberID.String(), "role": roleID.String()}, nil
}

func (daemon *Daemon) actionRoleEdit(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		GuildID     string  `json:"guild_id"`
		RoleID      string  `json:"role_id"`
		Name        *string `json:"name"`
		Color       *string `json:"color"`
		Hoist       *bool   `json:"hoist"`
		Mentionable *bool   `json:"mentionable"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	guildID, err := parseRequiredID("guild_id", body.GuildID)
	if err != nil {
		return nil, err
	}
	roleID, err := parseRequiredID("role_id", body.RoleID)
	if err != nil {
		return nil, err
	}
	update := disgodiscord.RoleUpdate{Name: body.Name, Hoist: body.Hoist, Mentionable: body.Mentionable}
	var updated []string
	if body.Name != nil {
		updated = append(updated, "name")
	}
	if body.Color != nil {
		c, err := voxdiscord.ParseHexColor(*body.Color)
		if err != nil {
			return nil, err
		}
		update.Color = &c
		updated = append(updated, "color")
	}
	if body.Hoist != nil {
		updated = append(updated, "hoist")
	}
	if body.Mentionable != nil {
		updated = append(updated, "mentionable")
	}
	if _, err := daemon.client.Rest.UpdateRole(guildID, roleID, update); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "role_id": roleID.String(), "updated": updated}, nil
}

// --- Webhooks ---

func (daemon *Daemon) actionWebhookList(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	hooks, err := daemon.client.Rest.GetWebhooks(channelID)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	out := make([]map[string]any, 0, len(hooks))
	for _, h := range hooks {
		urlStr := ""
		if inc, ok := h.(disgodiscord.IncomingWebhook); ok {
			urlStr = inc.URL()
		}
		out = append(out, map[string]any{"id": h.ID().String(), "name": h.Name(), "url": urlStr})
	}
	return map[string]any{"ok": true, "webhooks": out}, nil
}

func (daemon *Daemon) actionWebhookCreate(req RequestEnvelope) (map[string]any, error) {
	channelID, err := parseRequiredID("channel_id", strField(req.Raw, "channel_id"))
	if err != nil {
		return nil, err
	}
	name := strField(req.Raw, "name")
	if name == "" {
		name = "vox webhook"
	}
	hook, err := daemon.client.Rest.CreateWebhook(channelID, disgodiscord.WebhookCreate{Name: name})
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "webhook_id": hook.ID().String(), "name": hook.Name(), "url": hook.URL()}, nil
}

func (daemon *Daemon) actionWebhookDelete(req RequestEnvelope) (map[string]any, error) {
	webhookID, err := parseRequiredID("webhook_id", strField(req.Raw, "webhook_id"))
	if err != nil {
		return nil, err
	}
	if err := daemon.client.Rest.DeleteWebhook(webhookID); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true}, nil
}

// --- Scheduled Events ---

func (daemon *Daemon) actionEventList(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	evts, err := daemon.client.Rest.GetGuildScheduledEvents(guildID, true)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	out := make([]map[string]any, 0, len(evts))
	for _, e := range evts {
		var endTime any
		if e.ScheduledEndTime != nil {
			endTime = e.ScheduledEndTime.UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"id":          e.ID.String(),
			"name":        e.Name,
			"description": e.Description,
			"start_time":  e.ScheduledStartTime.UTC().Format(time.RFC3339),
			"end_time":    endTime,
			"status":      fmt.Sprintf("%d", e.Status),
			"user_count":  e.UserCount,
		})
	}
	return map[string]any{"ok": true, "events": out}, nil
}

func (daemon *Daemon) actionEventCreate(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	start, err := parseISO8601(strField(req.Raw, "start_time"))
	if err != nil {
		return nil, fmt.Errorf("invalid start_time: %w", err)
	}
	create := disgodiscord.GuildScheduledEventCreate{
		Name:               strField(req.Raw, "name"),
		Description:        strField(req.Raw, "description"),
		PrivacyLevel:       disgodiscord.ScheduledEventPrivacyLevelGuildOnly,
		ScheduledStartTime: start,
	}
	if endRaw := strField(req.Raw, "end_time"); endRaw != "" {
		end, err := parseISO8601(endRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid end_time: %w", err)
		}
		create.ScheduledEndTime = &end
	}
	if location := strField(req.Raw, "location"); location != "" {
		create.EntityType = disgodiscord.ScheduledEventEntityTypeExternal
		create.EntityMetaData = &disgodiscord.EntityMetaData{Location: location}
	} else if chRaw := strField(req.Raw, "channel_id"); chRaw != "" {
		chID, err := parseRequiredID("channel_id", chRaw)
		if err != nil {
			return nil, err
		}
		create.ChannelID = chID
		create.EntityType = disgodiscord.ScheduledEventEntityTypeVoice
	} else {
		return nil, errors.New("specify either location or channel_id")
	}
	ev, err := daemon.client.Rest.CreateGuildScheduledEvent(guildID, create)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return map[string]any{"ok": true, "event_id": ev.ID.String(), "name": ev.Name}, nil
}

// --- Servers ---

func (daemon *Daemon) actionServerList() (map[string]any, error) {
	guilds, err := voxdiscord.FetchAllGuilds(daemon.client.Rest)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(guilds))
	for _, g := range guilds {
		out = append(out, map[string]any{
			"id":           g.ID.String(),
			"name":         g.Name,
			"member_count": g.ApproximateMemberCount,
		})
	}
	return map[string]any{"ok": true, "servers": out}, nil
}

func (daemon *Daemon) actionServerInfo(req RequestEnvelope) (map[string]any, error) {
	guildID, err := parseRequiredID("guild_id", strField(req.Raw, "guild_id"))
	if err != nil {
		return nil, err
	}
	guild, err := daemon.client.Rest.GetGuild(guildID, true)
	if err != nil {
		return nil, voxdiscord.MapError(err)
	}
	channels, _ := daemon.client.Rest.GetGuildChannels(guildID)
	roles, _ := daemon.client.Rest.GetRoles(guildID)
	return map[string]any{
		"ok":            true,
		"id":            guild.ID.String(),
		"name":          guild.Name,
		"owner":         guild.OwnerID.String(),
		"member_count":  guild.ApproximateMemberCount,
		"channel_count": len(channels),
		"role_count":    len(roles),
		"created_at":    guild.ID.Time().UTC().Format(time.RFC3339),
	}, nil
}

// --- Voice ---

func (daemon *Daemon) resolveVoiceGuild(req RequestEnvelope) (snowflake.ID, snowflake.ID, error) {
	var channelID snowflake.ID
	if chRaw := strField(req.Raw, "channel_id"); chRaw != "" {
		id, err := parseRequiredID("channel_id", chRaw)
		if err != nil {
			return 0, 0, err
		}
		channelID = id
		if ch, ok := daemon.client.Caches.Channel(channelID); ok {
			return ch.GuildID(), channelID, nil
		}
		if ch, err := daemon.client.Rest.GetChannel(channelID); err == nil {
			if gch, ok := ch.(disgodiscord.GuildChannel); ok {
				return gch.GuildID(), channelID, nil
			}
		}
	}
	if gRaw := strField(req.Raw, "guild_id"); gRaw != "" {
		gid, err := parseRequiredID("guild_id", gRaw)
		if err != nil {
			return 0, 0, err
		}
		return gid, channelID, nil
	}
	return 0, 0, errors.New("specify channel_id or guild_id")
}

func (daemon *Daemon) actionVoiceConnect(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	guildID, channelID, err := daemon.resolveVoiceGuild(req)
	if err != nil || channelID == 0 {
		return nil, errors.New("voice_connect requires a valid channel_id")
	}
	if _, err := daemon.voice.Connect(ctx, guildID, channelID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "channel_id": channelID.String(), "guild_id": guildID.String()}, nil
}

func (daemon *Daemon) actionVoiceDisconnect(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	if err := daemon.voice.Disconnect(ctx, guildID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoiceMove(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	guildID, channelID, err := daemon.resolveVoiceGuild(req)
	if err != nil || channelID == 0 {
		return nil, errors.New("voice_move requires a valid channel_id")
	}
	if err := daemon.voice.Move(ctx, guildID, channelID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "channel_id": channelID.String()}, nil
}

func (daemon *Daemon) actionVoiceSpeak(ctx context.Context, req RequestEnvelope) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	if tts := strField(req.Raw, "tts"); tts != "" {
		daemon.voice.UpdateConfig(map[string]any{"tts_provider": tts})
	}
	speed := floatField(req.Raw, "speed", 1.0)
	if err := daemon.voice.Speak(ctx, guildID, strField(req.Raw, "text"), strField(req.Raw, "voice"), speed); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoicePlay(req RequestEnvelope) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	source := strField(req.Raw, "source")
	if source == "" {
		source = strField(req.Raw, "audio_url")
	}
	if err := daemon.voice.Play(guildID, source); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoiceControl(req RequestEnvelope, op string) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	switch op {
	case "stop":
		err = daemon.voice.Stop(guildID)
	case "pause":
		err = daemon.voice.Pause(guildID)
	case "resume":
		err = daemon.voice.Resume(guildID)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoiceListenStart(req RequestEnvelope) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	if stt := strField(req.Raw, "stt"); stt != "" {
		daemon.voice.UpdateConfig(map[string]any{"stt_provider": stt})
	}
	if err := daemon.voice.ListenStart(guildID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoiceListenStop(req RequestEnvelope) (map[string]any, error) {
	guildID, _, err := daemon.resolveVoiceGuild(req)
	if err != nil {
		return nil, err
	}
	daemon.voice.ListenStop(guildID)
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionVoiceWhere(req RequestEnvelope) (map[string]any, error) {
	userID, err := parseRequiredID("user_id", strField(req.Raw, "user_id"))
	if err != nil {
		return nil, err
	}
	guildFilter := strField(req.Raw, "guild_id")
	matches := make([]map[string]any, 0)
	for guild := range daemon.client.Caches.Guilds() {
		if guildFilter != "" && guild.ID.String() != guildFilter {
			continue
		}
		vs, ok := daemon.client.Caches.VoiceState(guild.ID, userID)
		if !ok || vs.ChannelID == nil {
			continue
		}
		matches = append(matches, map[string]any{
			"user_id":      userID.String(),
			"guild_id":     guild.ID.String(),
			"guild_name":   guild.Name,
			"channel_id":   vs.ChannelID.String(),
			"channel_name": daemon.channelName(*vs.ChannelID, false),
			"self_mute":    vs.SelfMute,
			"self_deaf":    vs.SelfDeaf,
			"mute":         vs.GuildMute,
			"deaf":         vs.GuildDeaf,
		})
	}
	return map[string]any{"ok": true, "matches": matches}, nil
}

func (daemon *Daemon) actionVoiceMembers(req RequestEnvelope) (map[string]any, error) {
	guildID, channelID, err := daemon.resolveVoiceGuild(req)
	if err != nil || channelID == 0 {
		return nil, errors.New("voice_members requires channel_id")
	}
	members := make([]map[string]any, 0)
	for vs := range daemon.client.Caches.VoiceStates(guildID) {
		if vs.ChannelID == nil || *vs.ChannelID != channelID {
			continue
		}
		userName := vs.UserID.String()
		displayName := userName
		if member, ok := daemon.client.Caches.Member(guildID, vs.UserID); ok {
			userName = voxdiscord.FormatUserTag(member.User)
			displayName = member.EffectiveName()
		}
		members = append(members, map[string]any{
			"user_id":      vs.UserID.String(),
			"user_name":    userName,
			"display_name": displayName,
			"self_mute":    vs.SelfMute,
			"self_deaf":    vs.SelfDeaf,
			"mute":         vs.GuildMute,
			"deaf":         vs.GuildDeaf,
		})
	}
	return map[string]any{
		"ok":           true,
		"channel_id":   channelID.String(),
		"channel_name": daemon.channelName(channelID, false),
		"guild_id":     guildID.String(),
		"count":        len(members),
		"members":      members,
	}, nil
}

func (daemon *Daemon) actionVoiceSetConfig(req RequestEnvelope) (map[string]any, error) {
	updates := make(map[string]any)
	if nested, ok := req.Raw["config"].(map[string]any); ok {
		maps.Copy(updates, nested)
	}
	flatMap := map[string]string{
		"tts":             "tts_provider",
		"stt":             "stt_provider",
		"tts_voice":       "tts_voice",
		"vad_enabled":     "vad_enabled",
		"playback_volume": "playback_volume",
		"speed":           "tts_speed",
	}
	for src, dst := range flatMap {
		if val, ok := req.Raw[src]; ok {
			updates[dst] = val
		}
	}
	updated := daemon.voice.UpdateConfig(updates)
	return map[string]any{"ok": true, "config": updated}, nil
}

// --- Workflows & Dashboards ---

func (daemon *Daemon) actionWorkflowStart(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID string                      `json:"channel_id"`
		UserID    string                      `json:"user_id"`
		Workflow  interact.WorkflowDefinition `json:"workflow"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	key, err := daemon.interact.WorkflowStart(channelID, body.UserID, body.Workflow)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workflow_key": key}, nil
}

func (daemon *Daemon) actionWorkflowCancel(req RequestEnvelope) (map[string]any, error) {
	cancelled := daemon.interact.WorkflowCancel(strField(req.Raw, "user_id"), strField(req.Raw, "workflow_id"))
	return map[string]any{"ok": true, "cancelled": cancelled}, nil
}

func (daemon *Daemon) actionDashboardCreate(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		ChannelID string                       `json:"channel_id"`
		Dashboard interact.DashboardDefinition `json:"dashboard"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	channelID, err := parseRequiredID("channel_id", body.ChannelID)
	if err != nil {
		return nil, err
	}
	id, err := daemon.interact.DashboardCreate(channelID, body.Dashboard)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "dashboard_id": id}, nil
}

func (daemon *Daemon) actionDashboardUpdate(req RequestEnvelope) (map[string]any, error) {
	var body struct {
		DashboardID string         `json:"dashboard_id"`
		Updates     map[string]any `json:"updates"`
	}
	if err := json.Unmarshal(req.Bytes, &body); err != nil {
		return nil, err
	}
	if err := daemon.interact.DashboardUpdate(body.DashboardID, body.Updates); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (daemon *Daemon) actionDashboardDelete(req RequestEnvelope) (map[string]any, error) {
	var channelID snowflake.ID
	if raw := strField(req.Raw, "channel_id"); raw != "" {
		channelID, _ = voxdiscord.ParseSnowflake(raw)
	}
	if err := daemon.interact.DashboardDelete(strField(req.Raw, "dashboard_id"), channelID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// --- Field parsing helpers ---

func parseRequiredID(field, raw string) (snowflake.ID, error) {
	id, ok := voxdiscord.ParseSnowflake(raw)
	if !ok {
		return 0, fmt.Errorf("invalid or missing %s: %q", field, raw)
	}
	return id, nil
}

func strField(raw map[string]any, key string) string {
	val, ok := raw[key]
	if !ok || val == nil {
		return ""
	}
	if s, ok := val.(string); ok {
		return s
	}
	return fmt.Sprint(val)
}

func intField(raw map[string]any, key string, fallback int) int {
	val, ok := raw[key]
	if !ok || val == nil {
		return fallback
	}
	switch n := val.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}

func floatField(raw map[string]any, key string, fallback float64) float64 {
	val, ok := raw[key]
	if !ok || val == nil {
		return fallback
	}
	if f, ok := val.(float64); ok {
		return f
	}
	return fallback
}

func boolField(raw map[string]any, key string, fallback bool) bool {
	val, ok := raw[key]
	if !ok || val == nil {
		return fallback
	}
	if b, ok := val.(bool); ok {
		return b
	}
	return fallback
}

func parseStringListField(raw any) []string {
	switch value := raw.(type) {
	case string:
		var out []string
		for part := range strings.SplitSeq(value, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

func parseISO8601(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("expected ISO 8601 timestamp, got %q", raw)
}
