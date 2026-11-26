// Package serve implements vox's persistent Discord bot daemon (`vox serve`)
// communicating via bidirectional JSONL over stdin and stdout.
package serve

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// DiscordMessageLimit is Discord's maximum character length per message.
	DiscordMessageLimit = 2000

	// StreamFlushInterval controls how often streaming responses edit Discord.
	StreamFlushInterval = 1500 * time.Millisecond

	// ComponentAutoDeferDelay gives external agents 2.5s to respond/open a modal
	// before vox defers the interaction to beat Discord's 3-second deadline.
	ComponentAutoDeferDelay = 2500 * time.Millisecond
)

// ActionSpec documents one JSONL action accepted on stdin by `vox serve`.
// Exposed via `vox schema` so AI agents can discover the exact protocol.
type ActionSpec struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Required    []string `json:"required,omitempty"`
}

// AllActions enumerates all 73 JSONL stdin actions supported by `vox serve`.
var AllActions = []ActionSpec{
	// Messaging (10)
	{Name: "send", Category: "messaging", Description: "Send a message with optional embed, components, and files", Required: []string{"channel_id"}},
	{Name: "reply", Category: "messaging", Description: "Reply to a message", Required: []string{"channel_id", "message_id"}},
	{Name: "edit", Category: "messaging", Description: "Edit an existing bot message", Required: []string{"channel_id", "message_id"}},
	{Name: "delete", Category: "messaging", Description: "Delete a message", Required: []string{"channel_id", "message_id"}},
	{Name: "message_list", Category: "messaging", Description: "List recent messages in a channel", Required: []string{"channel_id"}},
	{Name: "message_get", Category: "messaging", Description: "Fetch one message by ID", Required: []string{"channel_id", "message_id"}},
	{Name: "message_search", Category: "messaging", Description: "Search messages in a channel by content/author", Required: []string{"channel_id"}},
	{Name: "message_pin", Category: "messaging", Description: "Pin a message", Required: []string{"channel_id", "message_id"}},
	{Name: "message_unpin", Category: "messaging", Description: "Unpin a message", Required: []string{"channel_id", "message_id"}},
	{Name: "message_bulk_delete", Category: "messaging", Description: "Bulk-delete multiple messages", Required: []string{"channel_id", "message_ids"}},

	// Streaming (3)
	{Name: "stream_start", Category: "streaming", Description: "Start a live-updating streamed message", Required: []string{"channel_id"}},
	{Name: "stream_chunk", Category: "streaming", Description: "Append text to an active stream", Required: []string{"stream_id"}},
	{Name: "stream_end", Category: "streaming", Description: "Finalize stream and split overflow >2000 chars", Required: []string{"stream_id"}},

	// Interactions (4)
	{Name: "interaction_followup", Category: "interactions", Description: "Send a follow-up message to an interaction", Required: []string{"interaction_token"}},
	{Name: "interaction_respond", Category: "interactions", Description: "Send an immediate interaction response", Required: []string{"interaction_token"}},
	{Name: "interaction_edit", Category: "interactions", Description: "Edit the original interaction response", Required: []string{"interaction_token"}},
	{Name: "modal_send", Category: "interactions", Description: "Open a modal form in response to an interaction", Required: []string{"interaction_token", "title", "fields"}},

	// Typing & Presence (3)
	{Name: "typing_start", Category: "presence", Description: "Start persistent typing indicator in a channel", Required: []string{"channel_id"}},
	{Name: "typing_stop", Category: "presence", Description: "Stop typing indicator in a channel", Required: []string{"channel_id"}},
	{Name: "presence", Category: "presence", Description: "Update bot online status and activity"},

	// Reactions (3)
	{Name: "reaction_add", Category: "reactions", Description: "Add an emoji reaction to a message", Required: []string{"channel_id", "message_id", "emoji"}},
	{Name: "reaction_remove", Category: "reactions", Description: "Remove the bot's reaction from a message", Required: []string{"channel_id", "message_id", "emoji"}},
	{Name: "reaction_users", Category: "reactions", Description: "List users who reacted with an emoji", Required: []string{"channel_id", "message_id", "emoji"}},

	// Threads (7)
	{Name: "thread_create", Category: "threads", Description: "Create a thread from a message or standalone", Required: []string{"channel_id", "name"}},
	{Name: "thread_send", Category: "threads", Description: "Send a message inside a thread", Required: []string{"thread_id"}},
	{Name: "thread_list", Category: "threads", Description: "List active threads in a channel", Required: []string{"channel_id"}},
	{Name: "thread_archive", Category: "threads", Description: "Archive or unarchive a thread", Required: []string{"thread_id"}},
	{Name: "thread_rename", Category: "threads", Description: "Rename a thread", Required: []string{"thread_id", "name"}},
	{Name: "thread_add_member", Category: "threads", Description: "Add a user to a thread", Required: []string{"thread_id", "member_id"}},
	{Name: "thread_remove_member", Category: "threads", Description: "Remove a user from a thread", Required: []string{"thread_id", "member_id"}},

	// Polls (3)
	{Name: "poll_send", Category: "polls", Description: "Create a native Discord poll", Required: []string{"channel_id", "question", "answers"}},
	{Name: "poll_results", Category: "polls", Description: "Fetch vote counts for a poll", Required: []string{"channel_id", "message_id"}},
	{Name: "poll_end", Category: "polls", Description: "Expire a poll early", Required: []string{"channel_id", "message_id"}},

	// Channels & Forums (6)
	{Name: "channel_list", Category: "channels", Description: "List visible channels (sets visible_only: true)"},
	{Name: "channel_create", Category: "channels", Description: "Create a text/voice/category/forum channel", Required: []string{"guild_id", "name"}},
	{Name: "channel_info", Category: "channels", Description: "Fetch metadata for a channel", Required: []string{"channel_id"}},
	{Name: "channel_edit", Category: "channels", Description: "Update channel name, topic, slowmode, or nsfw", Required: []string{"channel_id"}},
	{Name: "channel_set_permissions", Category: "channels", Description: "Set role/member permission overwrites on a channel", Required: []string{"channel_id", "target_id"}},
	{Name: "forum_post", Category: "channels", Description: "Create a post in a forum channel", Required: []string{"channel_id", "title"}},

	// DMs (1)
	{Name: "dm_send", Category: "dm", Description: "Send a direct message to a user", Required: []string{"user_id"}},

	// Members & Roles (7)
	{Name: "member_list", Category: "members", Description: "List server members", Required: []string{"guild_id"}},
	{Name: "member_info", Category: "members", Description: "Inspect a guild member", Required: []string{"guild_id", "member_id"}},
	{Name: "member_timeout", Category: "members", Description: "Timeout or clear timeout for a member", Required: []string{"guild_id", "member_id"}},
	{Name: "role_list", Category: "roles", Description: "List roles (optional with_member_counts)", Required: []string{"guild_id"}},
	{Name: "role_assign", Category: "roles", Description: "Assign a role to a member", Required: []string{"guild_id", "member_id", "role_id"}},
	{Name: "role_remove", Category: "roles", Description: "Remove a role from a member", Required: []string{"guild_id", "member_id", "role_id"}},
	{Name: "role_edit", Category: "roles", Description: "Edit role name, color, hoist, or mentionable", Required: []string{"guild_id", "role_id"}},

	// Webhooks (3)
	{Name: "webhook_list", Category: "webhooks", Description: "List webhooks on a channel", Required: []string{"channel_id"}},
	{Name: "webhook_create", Category: "webhooks", Description: "Create a channel webhook", Required: []string{"channel_id"}},
	{Name: "webhook_delete", Category: "webhooks", Description: "Delete a webhook", Required: []string{"channel_id", "webhook_id"}},

	// Events (2)
	{Name: "event_list", Category: "events", Description: "List scheduled guild events", Required: []string{"guild_id"}},
	{Name: "event_create", Category: "events", Description: "Create a scheduled guild event", Required: []string{"guild_id", "name", "start_time"}},

	// Server (2)
	{Name: "server_list", Category: "server", Description: "List all servers the bot belongs to"},
	{Name: "server_info", Category: "server", Description: "Inspect a server", Required: []string{"guild_id"}},

	// Voice (14)
	{Name: "voice_connect", Category: "voice", Description: "Join a voice channel", Required: []string{"channel_id"}},
	{Name: "voice_disconnect", Category: "voice", Description: "Leave the active voice channel"},
	{Name: "voice_move", Category: "voice", Description: "Move bot to another voice channel", Required: []string{"channel_id"}},
	{Name: "voice_speak", Category: "voice", Description: "Synthesize TTS and play in voice"},
	{Name: "voice_play", Category: "voice", Description: "Play an audio file or URL in voice"},
	{Name: "voice_stop", Category: "voice", Description: "Stop voice playback"},
	{Name: "voice_pause", Category: "voice", Description: "Pause voice playback"},
	{Name: "voice_resume", Category: "voice", Description: "Resume voice playback"},
	{Name: "voice_listen_start", Category: "voice", Description: "Start STT transcription in voice"},
	{Name: "voice_listen_stop", Category: "voice", Description: "Stop STT transcription in voice"},
	{Name: "voice_status", Category: "voice", Description: "List active voice connections"},
	{Name: "voice_where", Category: "voice", Description: "Find which voice channel a user occupies", Required: []string{"user_id"}},
	{Name: "voice_members", Category: "voice", Description: "List members in a voice channel", Required: []string{"channel_id"}},
	{Name: "voice_set_config", Category: "voice", Description: "Update TTS/STT/volume voice config"},

	// Workflows & Dashboards (5)
	{Name: "workflow_start", Category: "interact", Description: "Start a multi-step interactive workflow", Required: []string{"channel_id", "user_id", "workflow"}},
	{Name: "workflow_cancel", Category: "interact", Description: "Cancel an active workflow", Required: []string{"user_id", "workflow_id"}},
	{Name: "dashboard_create", Category: "interact", Description: "Create a persistent embed or Components v2 dashboard", Required: []string{"channel_id", "dashboard"}},
	{Name: "dashboard_update", Category: "interact", Description: "Update dashboard state and re-render", Required: []string{"dashboard_id"}},
	{Name: "dashboard_delete", Category: "interact", Description: "Delete a dashboard and its Discord message", Required: []string{"dashboard_id"}},
}

// SlashParamSpec describes one parameter inside a --slash-commands JSON definition.
type SlashParamSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // string | integer | number | boolean
	Required    *bool  `json:"required,omitempty"`
}

// SlashCommandSpec describes one slash command registered on `vox serve` startup.
type SlashCommandSpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Params      []SlashParamSpec `json:"params,omitempty"`
}

// RequestEnvelope represents one decoded stdin JSONL command.
type RequestEnvelope struct {
	Action string         `json:"action"`
	ReqID  any            `json:"req_id,omitempty"`
	Raw    map[string]any `json:"-"`
	Bytes  []byte         `json:"-"`
}

// ParseRequest decodes one stdin JSONL line into a RequestEnvelope.
func ParseRequest(line []byte) (RequestEnvelope, error) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return RequestEnvelope{}, err
	}
	action, _ := raw["action"].(string)
	return RequestEnvelope{
		Action: strings.TrimSpace(action),
		ReqID:  raw["req_id"],
		Raw:    raw,
		Bytes:  line,
	}, nil
}

// SplitMessageChunks splits text into sequential chunks of at most limit runes
// (preserving UTF-8 boundaries) for Discord's 2000-character message cap.
func SplitMessageChunks(text string, limit int) []string {
	if limit <= 0 {
		limit = DiscordMessageLimit
	}
	if text == "" {
		return []string{"(empty response)"}
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	chunks := make([]string, 0, (len(runes)+limit-1)/limit)
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}
