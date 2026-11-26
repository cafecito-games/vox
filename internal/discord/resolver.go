package discord

import (
	"fmt"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/cafecito-games/vox/internal/output"
)

// ParseSnowflake parses a Discord snowflake string into a snowflake.ID.
func ParseSnowflake(value string) (snowflake.ID, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	id, err := snowflake.Parse(trimmed)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// FetchAllGuilds pages through GET /users/@me/guilds so bots in >200 guilds work reliably.
func FetchAllGuilds(restClient rest.Rest) ([]disgodiscord.OAuth2Guild, error) {
	var all []disgodiscord.OAuth2Guild
	var after snowflake.ID
	for {
		batch, err := restClient.GetCurrentUserGuilds("", 0, after, 200, 0, true)
		if err != nil {
			return nil, MapError(err)
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 200 {
			break
		}
		after = batch[len(batch)-1].ID
	}
	return all, nil
}

// ResolveGuild resolves a server by numeric ID or case-insensitive name and always
// returns the full RestGuild object (with roles and owner_id populated).
func ResolveGuild(restClient rest.Rest, identifier string) (*disgodiscord.RestGuild, error) {
	trimmed := strings.TrimSpace(identifier)
	if id, ok := ParseSnowflake(trimmed); ok {
		guild, err := restClient.GetGuild(id, true)
		if err != nil {
			return nil, MapError(err)
		}
		return guild, nil
	}

	guilds, err := FetchAllGuilds(restClient)
	if err != nil {
		return nil, err
	}
	matches := make([]disgodiscord.OAuth2Guild, 0, 1)
	for _, guild := range guilds {
		if strings.EqualFold(guild.Name, trimmed) {
			matches = append(matches, guild)
		}
	}
	chosen, err := uniqueMatch(matches, "Server", identifier, func(guild disgodiscord.OAuth2Guild) string {
		return guild.ID.String()
	})
	if err != nil {
		return nil, err
	}
	fullGuild, err := restClient.GetGuild(chosen.ID, true)
	if err != nil {
		return nil, MapError(err)
	}
	return fullGuild, nil
}

// ResolveChannel resolves a channel (or thread by ID) by numeric ID or
// case-insensitive name (ignoring a leading "#").
func ResolveChannel(restClient rest.Rest, identifier string) (disgodiscord.Channel, error) {
	normalized := strings.TrimPrefix(strings.TrimSpace(identifier), "#")
	if id, ok := ParseSnowflake(normalized); ok {
		channel, err := restClient.GetChannel(id)
		if err != nil {
			return nil, MapError(err)
		}
		return channel, nil
	}

	guilds, err := FetchAllGuilds(restClient)
	if err != nil {
		return nil, err
	}
	var matches []disgodiscord.Channel
	for _, guild := range guilds {
		channels, err := restClient.GetGuildChannels(guild.ID)
		if err != nil {
			continue
		}
		for _, channel := range channels {
			if strings.EqualFold(channel.Name(), normalized) {
				matches = append(matches, channel)
			}
		}
	}
	return uniqueMatch(matches, "Channel", identifier, func(ch disgodiscord.Channel) string {
		return ch.ID().String()
	})
}

// ResolveChannelInGuild resolves a channel within a single guild by numeric ID or name.
func ResolveChannelInGuild(restClient rest.Rest, guildID snowflake.ID, identifier string) (disgodiscord.GuildChannel, error) {
	normalized := strings.TrimPrefix(strings.TrimSpace(identifier), "#")
	channels, err := restClient.GetGuildChannels(guildID)
	if err != nil {
		return nil, MapError(err)
	}
	if id, ok := ParseSnowflake(normalized); ok {
		for _, channel := range channels {
			if channel.ID() == id {
				return channel, nil
			}
		}
		return nil, output.NewError("not_found", fmt.Sprintf("Channel not found: %s", identifier))
	}

	var matches []disgodiscord.GuildChannel
	for _, channel := range channels {
		if strings.EqualFold(channel.Name(), normalized) {
			matches = append(matches, channel)
		}
	}
	return uniqueMatch(matches, "Channel", identifier, func(ch disgodiscord.GuildChannel) string {
		return ch.ID().String()
	})
}

// ResolveThread resolves an active thread by numeric ID or case-insensitive name.
func ResolveThread(restClient rest.Rest, identifier string) (disgodiscord.GuildThread, error) {
	trimmed := strings.TrimSpace(identifier)
	if id, ok := ParseSnowflake(trimmed); ok {
		channel, err := restClient.GetChannel(id)
		if err != nil {
			return disgodiscord.GuildThread{}, MapError(err)
		}
		thread, ok := channel.(disgodiscord.GuildThread)
		if !ok {
			return disgodiscord.GuildThread{}, output.UsageError(fmt.Sprintf("Channel is not a thread: %s", identifier))
		}
		return thread, nil
	}

	guilds, err := FetchAllGuilds(restClient)
	if err != nil {
		return disgodiscord.GuildThread{}, err
	}
	var matches []disgodiscord.GuildThread
	for _, guild := range guilds {
		active, err := restClient.GetActiveGuildThreads(guild.ID)
		if err != nil {
			continue
		}
		for _, thread := range active.Threads {
			if strings.EqualFold(thread.Name(), trimmed) {
				matches = append(matches, thread)
			}
		}
	}
	return uniqueMatch(matches, "Thread", identifier, func(t disgodiscord.GuildThread) string {
		return t.ID().String()
	})
}

// ResolveRole resolves a role in guildID by numeric ID or case-insensitive name.
func ResolveRole(restClient rest.Rest, guildID snowflake.ID, identifier string) (disgodiscord.Role, error) {
	trimmed := strings.TrimSpace(identifier)
	roles, err := restClient.GetRoles(guildID)
	if err != nil {
		return disgodiscord.Role{}, MapError(err)
	}
	if id, ok := ParseSnowflake(trimmed); ok {
		for _, role := range roles {
			if role.ID == id {
				return role, nil
			}
		}
		return disgodiscord.Role{}, output.NewError("not_found", fmt.Sprintf("Role not found: %s", identifier))
	}
	var matches []disgodiscord.Role
	for _, role := range roles {
		if strings.EqualFold(role.Name, trimmed) {
			matches = append(matches, role)
		}
	}
	return uniqueMatch(matches, "Role", identifier, func(r disgodiscord.Role) string {
		return r.ID.String()
	})
}

// FetchGuildMembers retrieves up to limit members from a guild via REST pagination.
func FetchGuildMembers(restClient rest.Rest, guildID snowflake.ID, limit int) ([]disgodiscord.Member, error) {
	var all []disgodiscord.Member
	var after snowflake.ID
	for {
		pageSize := 1000
		if limit > 0 {
			remaining := limit - len(all)
			if remaining <= 0 {
				break
			}
			if remaining < pageSize {
				pageSize = remaining
			}
		}
		batch, err := restClient.GetMembers(guildID, pageSize, after)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < pageSize {
			break
		}
		after = batch[len(batch)-1].User.ID
	}
	return all, nil
}

// ResolveMember resolves a guild member by numeric ID or "@" / username lookup.
func ResolveMember(restClient rest.Rest, guildID snowflake.ID, identifier string) (*disgodiscord.Member, error) {
	trimmed := strings.TrimSpace(identifier)
	if id, ok := ParseSnowflake(trimmed); ok {
		member, err := restClient.GetMember(guildID, id)
		if err != nil {
			return nil, MapError(err)
		}
		return member, nil
	}

	normalized := strings.TrimPrefix(trimmed, "@")
	members, err := FetchGuildMembers(restClient, guildID, 0)
	if err != nil {
		if IsForbidden(err) {
			return nil, &output.CLIError{
				Code:     "missing_members_intent",
				Message:  "Member name lookup requires Discord's Server Members privileged intent. Use the numeric member ID or enable that intent for this application.",
				ExitCode: output.ExitError,
				Cause:    err,
			}
		}
		return nil, MapError(err)
	}

	var matches []disgodiscord.Member
	for _, member := range members {
		if strings.EqualFold(member.User.Username, normalized) || strings.EqualFold(FormatUserTag(member.User), normalized) {
			matches = append(matches, member)
		}
	}
	chosen, err := uniqueMatch(matches, "Member", identifier, func(m disgodiscord.Member) string {
		return m.User.ID.String()
	})
	if err != nil {
		return nil, err
	}
	return &chosen, nil
}

// ResolveUser resolves a Discord user by numeric ID or username across all guilds.
func ResolveUser(restClient rest.Rest, identifier string) (*disgodiscord.User, error) {
	trimmed := strings.TrimSpace(identifier)
	if id, ok := ParseSnowflake(trimmed); ok {
		user, err := restClient.GetUser(id)
		if err != nil {
			return nil, MapError(err)
		}
		return user, nil
	}

	normalized := strings.TrimPrefix(trimmed, "@")
	guilds, err := FetchAllGuilds(restClient)
	if err != nil {
		return nil, err
	}

	seen := make(map[snowflake.ID]disgodiscord.User)
	forbiddenSeen := false
	for _, guild := range guilds {
		members, err := FetchGuildMembers(restClient, guild.ID, 0)
		if err != nil {
			if IsForbidden(err) {
				forbiddenSeen = true
			}
			continue
		}
		for _, member := range members {
			if strings.EqualFold(member.User.Username, normalized) || strings.EqualFold(FormatUserTag(member.User), normalized) {
				seen[member.User.ID] = member.User
			}
		}
	}

	if len(seen) == 0 && forbiddenSeen {
		return nil, &output.CLIError{
			Code:     "missing_members_intent",
			Message:  "User name lookup requires Discord's Server Members privileged intent. Use the numeric user ID or enable that intent for this application.",
			ExitCode: output.ExitError,
		}
	}

	matches := make([]disgodiscord.User, 0, len(seen))
	for _, user := range seen {
		matches = append(matches, user)
	}
	chosen, err := uniqueMatch(matches, "User", identifier, func(u disgodiscord.User) string {
		return u.ID.String()
	})
	if err != nil {
		return nil, err
	}
	return &chosen, nil
}

// ResolveWebhook resolves a webhook within channelID by numeric ID or case-insensitive name.
func ResolveWebhook(restClient rest.Rest, channelID snowflake.ID, identifier string) (disgodiscord.Webhook, error) {
	trimmed := strings.TrimSpace(identifier)
	webhooks, err := restClient.GetWebhooks(channelID)
	if err != nil {
		return nil, MapError(err)
	}
	var matches []disgodiscord.Webhook
	for _, webhook := range webhooks {
		if webhook.ID().String() == trimmed {
			return webhook, nil
		}
		if strings.EqualFold(webhook.Name(), trimmed) {
			matches = append(matches, webhook)
		}
	}
	return uniqueMatch(matches, "Webhook", identifier, func(w disgodiscord.Webhook) string {
		return w.ID().String()
	})
}

// ResolveEmoji resolves a custom guild emoji by numeric ID or ":"-stripped name.
func ResolveEmoji(restClient rest.Rest, guildID snowflake.ID, identifier string) (disgodiscord.Emoji, error) {
	trimmed := strings.TrimSpace(identifier)
	emojis, err := restClient.GetEmojis(guildID)
	if err != nil {
		return disgodiscord.Emoji{}, MapError(err)
	}
	normalized := strings.Trim(trimmed, ":")
	var matches []disgodiscord.Emoji
	for _, emoji := range emojis {
		if emoji.ID.String() == trimmed {
			return emoji, nil
		}
		if strings.EqualFold(emoji.Name, normalized) {
			matches = append(matches, emoji)
		}
	}
	return uniqueMatch(matches, "Emoji", identifier, func(e disgodiscord.Emoji) string {
		return e.ID.String()
	})
}

// ResolveAutoModRule resolves a guild AutoMod rule by numeric ID or case-insensitive name.
func ResolveAutoModRule(restClient rest.Rest, guildID snowflake.ID, identifier string) (disgodiscord.AutoModerationRule, error) {
	trimmed := strings.TrimSpace(identifier)
	rules, err := restClient.GetAutoModerationRules(guildID)
	if err != nil {
		return disgodiscord.AutoModerationRule{}, MapError(err)
	}
	var matches []disgodiscord.AutoModerationRule
	for _, rule := range rules {
		if rule.ID.String() == trimmed {
			return rule, nil
		}
		if strings.EqualFold(rule.Name, trimmed) {
			matches = append(matches, rule)
		}
	}
	return uniqueMatch(matches, "AutoMod rule", identifier, func(r disgodiscord.AutoModerationRule) string {
		return r.ID.String()
	})
}

func uniqueMatch[T any](matches []T, kind, identifier string, idFunc func(T) string) (T, error) {
	var zero T
	switch len(matches) {
	case 0:
		return zero, output.NewError("not_found", fmt.Sprintf("%s not found: %s", kind, identifier))
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, match := range matches {
			ids[i] = idFunc(match)
		}
		return zero, output.UsageError(fmt.Sprintf(
			"Multiple %ss match %q (IDs: %s). Use an ID.",
			strings.ToLower(kind),
			identifier,
			strings.Join(ids, ", "),
		))
	}
}
