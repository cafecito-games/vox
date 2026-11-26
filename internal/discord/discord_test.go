package discord

import (
	"encoding/json"
	"testing"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGatewayIntentsMinimal(t *testing.T) {
	// Always includes Guilds, never requests privileged intents unless requested.
	base := BuildGatewayIntents(nil)
	assert.True(t, base.Has(gateway.IntentGuilds))
	assert.False(t, base.Has(gateway.IntentMessageContent))
	assert.False(t, base.Has(gateway.IntentGuildMembers))

	reactionsOnly := BuildGatewayIntents([]string{"reactions"})
	assert.True(t, reactionsOnly.Has(gateway.IntentGuildMessageReactions))
	assert.False(t, reactionsOnly.Has(gateway.IntentMessageContent))
	assert.False(t, reactionsOnly.Has(gateway.IntentGuildMembers))

	messagesOnly := BuildGatewayIntents([]string{"messages"})
	assert.True(t, messagesOnly.Has(gateway.IntentMessageContent))
	assert.True(t, messagesOnly.Has(gateway.IntentGuildMessages))
	assert.False(t, messagesOnly.Has(gateway.IntentGuildMembers))

	voiceOnly := BuildGatewayIntents([]string{"voice"})
	assert.True(t, voiceOnly.Has(gateway.IntentGuildVoiceStates))
	assert.False(t, voiceOnly.Has(gateway.IntentMessageContent))
}

func TestParseEventFilter(t *testing.T) {
	filter, err := ParseEventFilter("")
	require.NoError(t, err)
	assert.Empty(t, filter)
	assert.Equal(t, []string{"members", "messages", "reactions", "voice"}, GatewayFeaturesForEvents(filter))

	filter, err = ParseEventFilter("messages, edits, voice")
	require.NoError(t, err)
	assert.Equal(t, []string{"messages", "voice"}, GatewayFeaturesForEvents(filter))

	_, err = ParseEventFilter("messages,bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event category")
}

func TestPermissionParsingAnd2026Splits(t *testing.T) {
	mask, err := ParsePermissionNames([]string{"view_channel", "send_messages", "pin_messages"})
	require.NoError(t, err)
	assert.True(t, mask.Has(disgodiscord.PermissionViewChannel))
	assert.True(t, mask.Has(disgodiscord.PermissionSendMessages))
	assert.True(t, mask.Has(disgodiscord.PermissionPinMessages))

	names := PermissionNames(mask)
	assert.Equal(t, []string{"pin_messages", "send_messages", "view_channel"}, names)

	// Holding legacy manage_messages without pin_messages / bypass_slowmode flags both regressions.
	legacyOnly := disgodiscord.PermissionManageMessages
	splits := DetectPermissionSplits(legacyOnly)
	require.Len(t, splits, 2)
	assert.Equal(t, "pin_messages", splits[0].NewBit)
	assert.Equal(t, "bypass_slowmode", splits[1].NewBit)

	// Administrator implies everything, so zero split regressions.
	assert.Empty(t, DetectPermissionSplits(disgodiscord.PermissionAdministrator))

	inviteURL := BuildOAuthInviteURL(snowflake.ID(123456), disgodiscord.PermissionSendMessages)
	assert.Contains(t, inviteURL, "client_id=123456")
	assert.Contains(t, inviteURL, "permissions=2048")
}

func TestBuildEmbedAndComponents(t *testing.T) {
	embed, err := BuildEmbed(EmbedSpec{
		Title:       "Status",
		Description: "All systems operational",
		Color:       "#5865F2",
		Footer:      map[string]any{"text": "vox"},
		Author:      "Vox Bot",
		Fields:      []EmbedFieldSpec{{Name: "Online", Value: "42", Inline: true}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Status", embed.Title)
	assert.Equal(t, 0x5865F2, embed.Color)
	require.NotNil(t, embed.Footer)
	assert.Equal(t, "vox", embed.Footer.Text)
	require.NotNil(t, embed.Author)
	assert.Equal(t, "Vox Bot", embed.Author.Name)
	require.Len(t, embed.Fields, 1)

	_, err = ParseHexColor("not-hex")
	require.Error(t, err)

	field, err := ParseEmbedFieldFlag("Online::42::true")
	require.NoError(t, err)
	assert.Equal(t, EmbedFieldSpec{Name: "Online", Value: "42", Inline: true}, field)

	rawComponents := json.RawMessage(`[
		[{"type":"button","label":"OK","style":"success","custom_id":"ok"}],
		[{"type":"select","custom_id":"pick","placeholder":"Choose...","options":[{"label":"Go","value":"go"}]}]
	]`)
	rows, err := BuildComponentsFromRaw(rawComponents)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}
