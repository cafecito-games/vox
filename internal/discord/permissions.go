package discord

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/cafecito-games/vox/internal/output"
)

// PermissionNameMap maps canonical snake_case Discord permission names to disgo bitflags.
var PermissionNameMap = map[string]disgodiscord.Permissions{
	"create_instant_invite":    disgodiscord.PermissionCreateInstantInvite,
	"kick_members":             disgodiscord.PermissionKickMembers,
	"ban_members":              disgodiscord.PermissionBanMembers,
	"administrator":            disgodiscord.PermissionAdministrator,
	"manage_channels":          disgodiscord.PermissionManageChannels,
	"manage_guild":             disgodiscord.PermissionManageGuild,
	"add_reactions":            disgodiscord.PermissionAddReactions,
	"view_audit_log":           disgodiscord.PermissionViewAuditLog,
	"priority_speaker":         disgodiscord.PermissionPrioritySpeaker,
	"stream":                   disgodiscord.PermissionStream,
	"view_channel":             disgodiscord.PermissionViewChannel,
	"read_messages":            disgodiscord.PermissionViewChannel,
	"send_messages":            disgodiscord.PermissionSendMessages,
	"send_tts_messages":        disgodiscord.PermissionSendTTSMessages,
	"manage_messages":          disgodiscord.PermissionManageMessages,
	"embed_links":              disgodiscord.PermissionEmbedLinks,
	"attach_files":             disgodiscord.PermissionAttachFiles,
	"read_message_history":     disgodiscord.PermissionReadMessageHistory,
	"mention_everyone":         disgodiscord.PermissionMentionEveryone,
	"use_external_emojis":      disgodiscord.PermissionUseExternalEmojis,
	"external_emojis":          disgodiscord.PermissionUseExternalEmojis,
	"view_guild_insights":      disgodiscord.PermissionViewGuildInsights,
	"connect":                  disgodiscord.PermissionConnect,
	"speak":                    disgodiscord.PermissionSpeak,
	"mute_members":             disgodiscord.PermissionMuteMembers,
	"deafen_members":           disgodiscord.PermissionDeafenMembers,
	"move_members":             disgodiscord.PermissionMoveMembers,
	"use_voice_activation":     disgodiscord.PermissionUseVAD,
	"use_vad":                  disgodiscord.PermissionUseVAD,
	"change_nickname":          disgodiscord.PermissionChangeNickname,
	"manage_nicknames":         disgodiscord.PermissionManageNicknames,
	"manage_roles":             disgodiscord.PermissionManageRoles,
	"manage_webhooks":          disgodiscord.PermissionManageWebhooks,
	"manage_expressions":       disgodiscord.PermissionManageGuildExpressions,
	"manage_emojis":            disgodiscord.PermissionManageGuildExpressions,
	"use_application_cmds":     disgodiscord.PermissionUseApplicationCommands,
	"request_to_speak":         disgodiscord.PermissionRequestToSpeak,
	"manage_events":            disgodiscord.PermissionManageEvents,
	"manage_threads":           disgodiscord.PermissionManageThreads,
	"create_public_threads":    disgodiscord.PermissionCreatePublicThreads,
	"create_private_threads":   disgodiscord.PermissionCreatePrivateThreads,
	"use_external_stickers":    disgodiscord.PermissionUseExternalStickers,
	"send_messages_in_threads": disgodiscord.PermissionSendMessagesInThreads,
	"use_embedded_activities":  disgodiscord.PermissionUseEmbeddedActivities,
	"moderate_members":         disgodiscord.PermissionModerateMembers,
	"use_soundboard":           disgodiscord.PermissionUseSoundboard,
	"create_expressions":       disgodiscord.PermissionCreateGuildExpressions,
	"create_events":            disgodiscord.PermissionCreateEvents,
	"use_external_sounds":      disgodiscord.PermissionUseExternalSounds,
	"send_voice_messages":      disgodiscord.PermissionSendVoiceMessages,
	"send_polls":               disgodiscord.PermissionSendPolls,
	"use_external_apps":        disgodiscord.PermissionUseExternalApps,
	"pin_messages":             disgodiscord.PermissionPinMessages,
	"bypass_slowmode":          disgodiscord.PermissionBypassSlowmode,
}

// canonicalPermissionOrder defines the non-alias permission names used when
// serializing permission bitmasks back to sorted name slices.
var canonicalPermissionOrder = []string{
	"create_instant_invite",
	"kick_members",
	"ban_members",
	"administrator",
	"manage_channels",
	"manage_guild",
	"add_reactions",
	"view_audit_log",
	"priority_speaker",
	"stream",
	"view_channel",
	"send_messages",
	"send_tts_messages",
	"manage_messages",
	"embed_links",
	"attach_files",
	"read_message_history",
	"mention_everyone",
	"use_external_emojis",
	"view_guild_insights",
	"connect",
	"speak",
	"mute_members",
	"deafen_members",
	"move_members",
	"use_voice_activation",
	"change_nickname",
	"manage_nicknames",
	"manage_roles",
	"manage_webhooks",
	"manage_expressions",
	"use_application_cmds",
	"request_to_speak",
	"manage_events",
	"manage_threads",
	"create_public_threads",
	"create_private_threads",
	"use_external_stickers",
	"send_messages_in_threads",
	"use_embedded_activities",
	"moderate_members",
	"use_soundboard",
	"create_expressions",
	"create_events",
	"use_external_sounds",
	"send_voice_messages",
	"send_polls",
	"use_external_apps",
	"pin_messages",
	"bypass_slowmode",
}

// VoxPermissions lists the 28 Discord permissions checked by `vox doctor --server`.
var VoxPermissions = []string{
	"view_channel",
	"read_message_history",
	"send_messages",
	"embed_links",
	"attach_files",
	"add_reactions",
	"send_polls",
	"create_public_threads",
	"manage_messages",
	"pin_messages",
	"bypass_slowmode",
	"manage_channels",
	"create_instant_invite",
	"manage_roles",
	"manage_nicknames",
	"kick_members",
	"ban_members",
	"moderate_members",
	"move_members",
	"manage_webhooks",
	"manage_guild",
	"view_audit_log",
	"create_expressions",
	"manage_expressions",
	"create_events",
	"manage_events",
	"connect",
	"speak",
}

// PermissionSplit describes a Discord 2026 permission split where a bot invited
// before the cutoff holds the legacy bit but silently lost the new capability.
type PermissionSplit struct {
	NewBit        string
	LegacyBit     string
	EnforcedSince string
	Affected      string
}

// PermissionSplits enumerates Discord's 2026 permission splits checked by `vox doctor --server`.
var PermissionSplits = []PermissionSplit{
	{
		NewBit:        "pin_messages",
		LegacyBit:     "manage_messages",
		EnforcedSince: "2026-01",
		Affected:      "message pin, message unpin",
	},
	{
		NewBit:        "bypass_slowmode",
		LegacyBit:     "manage_messages",
		EnforcedSince: "2026-02-23",
		Affected:      "posting while slowmode is active",
	},
	{
		NewBit:        "create_expressions",
		LegacyBit:     "manage_expressions",
		EnforcedSince: "2026-02-23",
		Affected:      "emoji upload",
	},
	{
		NewBit:        "create_events",
		LegacyBit:     "manage_events",
		EnforcedSince: "2026-02-23",
		Affected:      "event create",
	},
}

// InviteBundle groups related Discord permissions for the interactive `vox setup` wizard.
type InviteBundle struct {
	Name        string
	Description string
	Default     bool
	Permissions []string
}

// InviteBundles lists the capability bundles offered during `vox setup`.
var InviteBundles = []InviteBundle{
	{
		Name:        "messaging",
		Description: "Send, read, react, pin-free messaging, polls, and threads",
		Default:     true,
		Permissions: []string{
			"view_channel", "read_message_history", "send_messages", "embed_links",
			"attach_files", "add_reactions", "send_polls", "create_public_threads",
		},
	},
	{
		Name:        "moderation",
		Description: "Delete/pin messages, bypass slowmode, kick, ban, timeout, nicknames",
		Default:     false,
		Permissions: []string{
			"manage_messages", "pin_messages", "bypass_slowmode",
			"kick_members", "ban_members", "moderate_members", "manage_nicknames",
		},
	},
	{
		Name:        "voice",
		Description: "Connect, speak, and move members between voice channels",
		Default:     false,
		Permissions: []string{"connect", "speak", "move_members"},
	},
	{
		Name:        "admin",
		Description: "Channels, roles, server settings, audit log, webhooks, invites, emoji, events",
		Default:     false,
		Permissions: []string{
			"manage_channels", "manage_roles", "manage_guild", "view_audit_log",
			"manage_webhooks", "create_instant_invite", "create_expressions",
			"manage_expressions", "create_events", "manage_events",
		},
	},
}

// ParsePermissionNames converts snake_case or hyphenated permission names into a bitmask.
func ParsePermissionNames(names []string) (disgodiscord.Permissions, error) {
	var mask disgodiscord.Permissions
	for _, raw := range names {
		normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "-", "_")
		if normalized == "" {
			continue
		}
		bit, ok := PermissionNameMap[normalized]
		if !ok {
			return 0, output.UsageError(fmt.Sprintf("unknown Discord permission %q", raw))
		}
		mask = mask.Add(bit)
	}
	return mask, nil
}

// PermissionNames returns the sorted canonical permission names set in mask.
func PermissionNames(mask disgodiscord.Permissions) []string {
	names := make([]string, 0)
	for _, name := range canonicalPermissionOrder {
		if mask.Has(PermissionNameMap[name]) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// DetectPermissionSplits returns any 2026 Discord permission splits where the bot
// holds the legacy permission bit but lacks the newly split bit.
func DetectPermissionSplits(perms disgodiscord.Permissions) []PermissionSplit {
	if perms.Has(disgodiscord.PermissionAdministrator) {
		return nil
	}
	var regressions []PermissionSplit
	for _, split := range PermissionSplits {
		legacy := PermissionNameMap[split.LegacyBit]
		current := PermissionNameMap[split.NewBit]
		if perms.Has(legacy) && !perms.Has(current) {
			regressions = append(regressions, split)
		}
	}
	return regressions
}

// BuildOAuthInviteURL generates a Discord OAuth2 bot invite URL with the exact
// requested permissions bitmask.
func BuildOAuthInviteURL(botID snowflake.ID, perms disgodiscord.Permissions) string {
	query := url.Values{}
	query.Set("client_id", botID.String())
	query.Set("scope", "bot applications.commands")
	query.Set("permissions", strconv.FormatInt(int64(perms), 10))
	return "https://discord.com/oauth2/authorize?" + query.Encode()
}

// CheckTriggeredByPermission verifies that triggeringUserID holds requiredPermission
// in guildID (for `--triggered-by` checks on moderation commands).
//
// Returns (verified, error). When the member cannot be fetched (for example,
// missing Server Members intent), verified is false and error is nil so the caller
// can emit a warning and record an audit note rather than hard-failing.
func CheckTriggeredByPermission(
	restClient rest.Rest,
	guild *disgodiscord.RestGuild,
	triggeringUserID snowflake.ID,
	requiredPermission string,
) (bool, string, error) {
	if guild.OwnerID == triggeringUserID {
		return true, triggeringUserID.String(), nil
	}
	member, fetchErr := restClient.GetMember(guild.ID, triggeringUserID)
	if fetchErr != nil || member == nil {
		return false, "", nil //nolint:nilerr // intentionally fail open when member lookup is unavailable
	}
	perms, permErr := restClient.GetGuildMemberPermissions(guild.ID, triggeringUserID)
	if permErr != nil {
		return false, FormatUserTag(member.User), nil //nolint:nilerr // intentionally fail open when permission lookup is unavailable
	}
	if perms.Has(disgodiscord.PermissionAdministrator) {
		return true, FormatUserTag(member.User), nil
	}

	mapped := map[string]disgodiscord.Permissions{
		"kick":             disgodiscord.PermissionKickMembers,
		"ban":              disgodiscord.PermissionBanMembers,
		"manage_channels":  disgodiscord.PermissionManageChannels,
		"manage_roles":     disgodiscord.PermissionManageRoles,
		"manage_messages":  disgodiscord.PermissionManageMessages,
		"moderate_members": disgodiscord.PermissionModerateMembers,
		"manage_nicknames": disgodiscord.PermissionManageNicknames,
	}
	requiredBit, ok := mapped[requiredPermission]
	if !ok {
		return false, FormatUserTag(member.User), fmt.Errorf("unknown required permission %q", requiredPermission)
	}
	if !perms.Has(requiredBit) {
		return true, FormatUserTag(member.User), output.PermissionError(
			fmt.Sprintf("User %s does not have %q permission in %s.", FormatUserTag(member.User), requiredPermission, guild.Name),
			nil,
		)
	}
	return true, FormatUserTag(member.User), nil
}
