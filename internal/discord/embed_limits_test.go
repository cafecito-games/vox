package discord

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/output"
)

func TestValidateEmbedsNamesEveryOffender(t *testing.T) {
	t.Run("too many embeds", func(t *testing.T) {
		embeds := make([]disgodiscord.Embed, MaxEmbedsPerMessage+1)
		err := ValidateEmbeds(embeds)
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
		assert.EqualError(t, err, "message has 11 embeds (limit 10)")
	})

	t.Run("overlong title", func(t *testing.T) {
		err := ValidateEmbed(disgodiscord.Embed{Title: strings.Repeat("a", MaxEmbedTitleLength+1)})
		require.Error(t, err)
		assert.EqualError(t, err, "embed title is 257 characters (limit 256)")
	})

	t.Run("overlong description", func(t *testing.T) {
		err := ValidateEmbed(disgodiscord.Embed{Description: strings.Repeat("b", MaxEmbedDescriptionLength+1)})
		require.Error(t, err)
		assert.EqualError(t, err, "embed description is 4097 characters (limit 4096)")
	})

	t.Run("overlong footer", func(t *testing.T) {
		err := ValidateEmbed(disgodiscord.Embed{Footer: &disgodiscord.EmbedFooter{Text: strings.Repeat("f", MaxEmbedFooterTextLength+1)}})
		require.Error(t, err)
		assert.EqualError(t, err, "embed footer text is 2049 characters (limit 2048)")
	})

	t.Run("overlong author", func(t *testing.T) {
		err := ValidateEmbed(disgodiscord.Embed{Author: &disgodiscord.EmbedAuthor{Name: strings.Repeat("u", MaxEmbedAuthorNameLength+1)}})
		require.Error(t, err)
		assert.EqualError(t, err, "embed author name is 257 characters (limit 256)")
	})

	t.Run("too many fields", func(t *testing.T) {
		fields := make([]disgodiscord.EmbedField, MaxEmbedFieldCount+1)
		for index := range fields {
			fields[index] = disgodiscord.EmbedField{Name: "k", Value: "v"}
		}
		err := ValidateEmbed(disgodiscord.Embed{Fields: fields})
		require.Error(t, err)
		assert.EqualError(t, err, "embed has 26 fields (limit 25)")
	})

	t.Run("overlong field name", func(t *testing.T) {
		err := ValidateEmbed(disgodiscord.Embed{
			Fields: []disgodiscord.EmbedField{{Name: strings.Repeat("n", MaxEmbedFieldNameLength+1), Value: "ok"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "embed field 1")
		assert.Contains(t, err.Error(), "name is 257 characters (limit 256)")
	})

	t.Run("overlong field value names field index and label", func(t *testing.T) {
		fields := make([]disgodiscord.EmbedField, 7)
		for index := range 6 {
			fields[index] = disgodiscord.EmbedField{Name: "ok", Value: "fine"}
		}
		fields[6] = disgodiscord.EmbedField{Name: "disk /var", Value: strings.Repeat("x", 1419)}
		err := ValidateEmbed(disgodiscord.Embed{Fields: fields})
		require.Error(t, err)
		assert.EqualError(t, err, `embed field 7 "disk /var" value is 1419 characters (limit 1024)`)
	})

	t.Run("over 6000 total characters across single embed", func(t *testing.T) {
		fields := make([]disgodiscord.EmbedField, 6)
		for index := range fields {
			fields[index] = disgodiscord.EmbedField{Name: "f", Value: strings.Repeat("v", 1000)}
		}
		err := ValidateEmbed(disgodiscord.Embed{Title: "Summary", Fields: fields})
		require.Error(t, err)
		assert.EqualError(t, err, "embed total text is 6013 characters (limit 6000)")
	})

	t.Run("over 6000 total characters across multiple embeds", func(t *testing.T) {
		embeds := []disgodiscord.Embed{
			{Description: strings.Repeat("a", 3500)},
			{Description: strings.Repeat("b", 2600)},
		}
		err := ValidateEmbeds(embeds)
		require.Error(t, err)
		assert.EqualError(t, err, "embeds total text is 6100 characters across 2 embeds (limit 6000)")
	})

	t.Run("counts Unicode runes instead of UTF-8 bytes", func(t *testing.T) {
		// 256 multi-byte characters (each 3 bytes in UTF-8) fit within the 256-character limit.
		err := ValidateEmbed(disgodiscord.Embed{Title: strings.Repeat("✓", MaxEmbedTitleLength)})
		require.NoError(t, err)
	})
}

func TestTruncateEmbedsSatisfiesAllLimits(t *testing.T) {
	fields := make([]disgodiscord.EmbedField, 30)
	for index := range fields {
		fields[index] = disgodiscord.EmbedField{
			Name:  strings.Repeat("N", 300),
			Value: strings.Repeat("V", 1200),
		}
	}
	embeds := make([]disgodiscord.Embed, 12)
	for index := range embeds {
		embeds[index] = disgodiscord.Embed{
			Title:       strings.Repeat("T", 300),
			Description: strings.Repeat("D", 5000),
			Footer:      &disgodiscord.EmbedFooter{Text: strings.Repeat("F", 2500)},
			Author:      &disgodiscord.EmbedAuthor{Name: strings.Repeat("A", 300)},
			Fields:      fields,
		}
	}

	truncated := TruncateEmbeds(embeds)
	require.Len(t, truncated, MaxEmbedsPerMessage)
	require.NoError(t, ValidateEmbeds(truncated))

	assert.Equal(t, MaxEmbedTitleLength, utf8.RuneCountInString(truncated[0].Title))
	assert.True(t, strings.HasSuffix(truncated[0].Title, "…"))
}

func TestTruncateEmbedsTrimsLastFieldBeforeDropping(t *testing.T) {
	embed := disgodiscord.Embed{
		Description: strings.Repeat("d", 4000),
		Fields: []disgodiscord.EmbedField{
			{Name: "first", Value: strings.Repeat("a", 1000)},
			{Name: "second", Value: strings.Repeat("b", 1000)},
			{Name: "third", Value: strings.Repeat("c", 500)},
		},
	}

	truncated := TruncateEmbeds([]disgodiscord.Embed{embed})
	require.Len(t, truncated, 1)
	require.NoError(t, ValidateEmbeds(truncated))
	assert.LessOrEqual(t, EmbedCharacterCount(truncated[0]), MaxEmbedTotalCharacters)
	assert.Len(t, truncated[0].Fields, 2)
	assert.True(t, strings.HasSuffix(truncated[0].Fields[1].Value, "…"))
}

func TestEscapeMarkdownAndBuildEmbedWithOptions(t *testing.T) {
	escaped := EscapeMarkdown(`prod-vm-1 /var/lib [click](https://evil.test) *bold* _it_ ` + "`code`" + ` ||sp|| #h <@123> \x`)
	assert.Equal(t, `prod-vm-1 /var/lib \[click\]\(https://evil.test\) \*bold\* \_it\_ \`+"`"+`code\`+"`"+` \|\|sp\|\| \#h \<@123\> \\x`, escaped)

	// Escaping runs before truncation so the final field value still fits within 1024 characters.
	embed, err := BuildEmbedWithOptions(EmbedSpec{
		Title: "Disk check",
		Fields: []EmbedFieldSpec{{
			Name:  "disk_/var*",
			Value: strings.Repeat("*a*", 400),
		}},
	}, EmbedBuildOptions{
		EscapeFields: true,
		Truncate:     true,
	})
	require.NoError(t, err)
	require.Len(t, embed.Fields, 1)
	assert.Equal(t, `disk\_/var\*`, embed.Fields[0].Name)
	assert.Equal(t, MaxEmbedFieldValueLength, utf8.RuneCountInString(embed.Fields[0].Value))
	assert.True(t, strings.HasSuffix(embed.Fields[0].Value, "…"))
}

func TestPrepareRawPayloadEmbedsPreservesExtraFields(t *testing.T) {
	raw := map[string]json.RawMessage{
		"embeds": json.RawMessage(`[{
			"title": "Alert",
			"color": 15158332,
			"timestamp": "2026-11-27T00:00:00Z",
			"footer": {"text": "monitor", "icon_url": "https://example.test/icon.png"},
			"fields": [{"name": "host_1", "value": "` + strings.Repeat("x", 1200) + `", "inline": true}]
		}]`),
	}

	// Without --truncate, validation rejects the oversize field.
	err := PrepareRawPayloadEmbeds(raw, EmbedBuildOptions{})
	require.Error(t, err)
	assert.EqualError(t, err, `embed field 1 "host_1" value is 1200 characters (limit 1024)`)

	// With --escape-fields and --truncate, it escapes, clips, and preserves extra properties.
	require.NoError(t, PrepareRawPayloadEmbeds(raw, EmbedBuildOptions{
		EscapeFields: true,
		Truncate:     true,
	}))
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw["embeds"], &decoded))
	require.Len(t, decoded, 1)
	assert.Equal(t, float64(15158332), decoded[0]["color"])
	assert.Equal(t, "2026-11-27T00:00:00Z", decoded[0]["timestamp"])
	footer := decoded[0]["footer"].(map[string]any)
	assert.Equal(t, "https://example.test/icon.png", footer["icon_url"])
	fields := decoded[0]["fields"].([]any)
	field := fields[0].(map[string]any)
	assert.Equal(t, `host\_1`, field["name"])
	assert.Equal(t, true, field["inline"])
	assert.Equal(t, MaxEmbedFieldValueLength, utf8.RuneCountInString(field["value"].(string)))
}
