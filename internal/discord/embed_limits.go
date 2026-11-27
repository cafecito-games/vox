package discord

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	disgodiscord "github.com/disgoorg/disgo/discord"

	"github.com/cafecito-games/vox/internal/output"
)

// Documented Discord embed limits (measured in Unicode characters).
const (
	MaxEmbedsPerMessage       = 10
	MaxEmbedTitleLength       = 256
	MaxEmbedDescriptionLength = 4096
	MaxEmbedFieldCount        = 25
	MaxEmbedFieldNameLength   = 256
	MaxEmbedFieldValueLength  = 1024
	MaxEmbedFooterTextLength  = 2048
	MaxEmbedAuthorNameLength  = 256
	MaxEmbedTotalCharacters   = 6000
)

// EmbedBuildOptions controls embed validation, truncation, and markdown escaping.
type EmbedBuildOptions struct {
	Truncate     bool
	EscapeFields bool
}

// EscapeMarkdown escapes Discord markdown formatting and link-injection characters
// in untrusted text so values render literally inside embeds.
func EscapeMarkdown(text string) string {
	if text == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\\', '*', '_', '`', '~', '|', '>', '<', '[', ']', '(', ')', '#':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// EmbedCharacterCount returns the total number of characters counted by Discord
// toward the 6000-character embed text limit for a single embed.
func EmbedCharacterCount(embed disgodiscord.Embed) int {
	total := utf8.RuneCountInString(embed.Title) + utf8.RuneCountInString(embed.Description)
	if embed.Footer != nil {
		total += utf8.RuneCountInString(embed.Footer.Text)
	}
	if embed.Author != nil {
		total += utf8.RuneCountInString(embed.Author.Name)
	}
	for _, field := range embed.Fields {
		total += utf8.RuneCountInString(field.Name) + utf8.RuneCountInString(field.Value)
	}
	return total
}

// ValidateEmbed verifies that one embed satisfies Discord's documented limits.
func ValidateEmbed(embed disgodiscord.Embed) error {
	return ValidateEmbeds([]disgodiscord.Embed{embed})
}

// ValidateEmbeds verifies that a slice of embeds satisfies Discord's documented
// per-embed and per-message limits, returning a usage error naming the offender.
func ValidateEmbeds(embeds []disgodiscord.Embed) error {
	if len(embeds) > MaxEmbedsPerMessage {
		return output.UsageError(fmt.Sprintf("message has %d embeds (limit %d)", len(embeds), MaxEmbedsPerMessage))
	}
	totalCharacters := 0
	for embedIndex, embed := range embeds {
		label := "embed"
		if len(embeds) > 1 {
			label = fmt.Sprintf("embed %d", embedIndex+1)
		}
		if titleLength := utf8.RuneCountInString(embed.Title); titleLength > MaxEmbedTitleLength {
			return output.UsageError(fmt.Sprintf("%s title is %d characters (limit %d)", label, titleLength, MaxEmbedTitleLength))
		}
		if descriptionLength := utf8.RuneCountInString(embed.Description); descriptionLength > MaxEmbedDescriptionLength {
			return output.UsageError(fmt.Sprintf("%s description is %d characters (limit %d)", label, descriptionLength, MaxEmbedDescriptionLength))
		}
		if embed.Footer != nil {
			if footerLength := utf8.RuneCountInString(embed.Footer.Text); footerLength > MaxEmbedFooterTextLength {
				return output.UsageError(fmt.Sprintf("%s footer text is %d characters (limit %d)", label, footerLength, MaxEmbedFooterTextLength))
			}
		}
		if embed.Author != nil {
			if authorLength := utf8.RuneCountInString(embed.Author.Name); authorLength > MaxEmbedAuthorNameLength {
				return output.UsageError(fmt.Sprintf("%s author name is %d characters (limit %d)", label, authorLength, MaxEmbedAuthorNameLength))
			}
		}
		if len(embed.Fields) > MaxEmbedFieldCount {
			return output.UsageError(fmt.Sprintf("%s has %d fields (limit %d)", label, len(embed.Fields), MaxEmbedFieldCount))
		}
		for fieldIndex, field := range embed.Fields {
			fieldNumber := fieldIndex + 1
			if nameLength := utf8.RuneCountInString(field.Name); nameLength > MaxEmbedFieldNameLength {
				return output.UsageError(fmt.Sprintf(
					"%s field %d %q name is %d characters (limit %d)",
					label, fieldNumber, truncateRunes(field.Name, 32), nameLength, MaxEmbedFieldNameLength,
				))
			}
			if valueLength := utf8.RuneCountInString(field.Value); valueLength > MaxEmbedFieldValueLength {
				return output.UsageError(fmt.Sprintf(
					"%s field %d %q value is %d characters (limit %d)",
					label, fieldNumber, field.Name, valueLength, MaxEmbedFieldValueLength,
				))
			}
		}
		totalCharacters += EmbedCharacterCount(embed)
	}
	if totalCharacters > MaxEmbedTotalCharacters {
		if len(embeds) == 1 {
			return output.UsageError(fmt.Sprintf("embed total text is %d characters (limit %d)", totalCharacters, MaxEmbedTotalCharacters))
		}
		return output.UsageError(fmt.Sprintf("embeds total text is %d characters across %d embeds (limit %d)", totalCharacters, len(embeds), MaxEmbedTotalCharacters))
	}
	return nil
}

// TruncateEmbeds returns a copy of embeds clipped to satisfy every Discord embed limit.
func TruncateEmbeds(embeds []disgodiscord.Embed) []disgodiscord.Embed {
	if len(embeds) == 0 {
		return nil
	}
	count := min(len(embeds), MaxEmbedsPerMessage)
	result := make([]disgodiscord.Embed, count)
	totalCharacters := 0

	for index, embed := range embeds[:count] {
		embed.Title = truncateRunes(embed.Title, MaxEmbedTitleLength)
		embed.Description = truncateRunes(embed.Description, MaxEmbedDescriptionLength)
		if embed.Footer != nil {
			footerCopy := *embed.Footer
			footerCopy.Text = truncateRunes(footerCopy.Text, MaxEmbedFooterTextLength)
			embed.Footer = &footerCopy
		}
		if embed.Author != nil {
			authorCopy := *embed.Author
			authorCopy.Name = truncateRunes(authorCopy.Name, MaxEmbedAuthorNameLength)
			embed.Author = &authorCopy
		}
		if len(embed.Fields) > 0 {
			fieldCount := min(len(embed.Fields), MaxEmbedFieldCount)
			fields := make([]disgodiscord.EmbedField, fieldCount)
			for fieldIndex, field := range embed.Fields[:fieldCount] {
				field.Name = truncateRunes(field.Name, MaxEmbedFieldNameLength)
				field.Value = truncateRunes(field.Value, MaxEmbedFieldValueLength)
				fields[fieldIndex] = field
			}
			embed.Fields = fields
		}
		result[index] = embed
		totalCharacters += EmbedCharacterCount(embed)
	}

	excess := totalCharacters - MaxEmbedTotalCharacters
	if excess <= 0 {
		return result
	}

	// Trim fields first, working backward through embeds and fields so the most
	// prominent summary fields stay intact while keeping non-empty field values.
	for embedIndex := len(result) - 1; embedIndex >= 0 && excess > 0; embedIndex-- {
		fields := result[embedIndex].Fields
		for len(fields) > 0 && excess > 0 {
			lastIndex := len(fields) - 1
			nameLength := utf8.RuneCountInString(fields[lastIndex].Name)
			valueLength := utf8.RuneCountInString(fields[lastIndex].Value)
			if valueLength-excess >= 1 {
				fields[lastIndex].Value = truncateRunes(fields[lastIndex].Value, valueLength-excess)
				excess = 0
				break
			}
			excess -= nameLength + valueLength
			fields = fields[:lastIndex]
		}
		result[embedIndex].Fields = fields
	}

	// If total text still exceeds 6000 characters, trim description, footer,
	// title, and author name in reverse embed order.
	trimText := func(text string) string {
		if excess <= 0 || text == "" {
			return text
		}
		length := utf8.RuneCountInString(text)
		if length <= excess {
			excess -= length
			return ""
		}
		target := length - excess
		excess = 0
		return truncateRunes(text, target)
	}

	for embedIndex := len(result) - 1; embedIndex >= 0 && excess > 0; embedIndex-- {
		result[embedIndex].Description = trimText(result[embedIndex].Description)
	}
	for embedIndex := len(result) - 1; embedIndex >= 0 && excess > 0; embedIndex-- {
		if result[embedIndex].Footer != nil {
			result[embedIndex].Footer.Text = trimText(result[embedIndex].Footer.Text)
		}
	}
	for embedIndex := len(result) - 1; embedIndex >= 0 && excess > 0; embedIndex-- {
		result[embedIndex].Title = trimText(result[embedIndex].Title)
	}
	for embedIndex := len(result) - 1; embedIndex >= 0 && excess > 0; embedIndex-- {
		if result[embedIndex].Author != nil {
			result[embedIndex].Author.Name = trimText(result[embedIndex].Author.Name)
		}
	}

	return result
}

// PrepareRawPayloadEmbeds validates (and optionally escapes and truncates) the
// "embeds" array in a raw Discord webhook JSON payload while preserving all
// other embed properties.
func PrepareRawPayloadEmbeds(payload map[string]json.RawMessage, options EmbedBuildOptions) error {
	rawEmbeds, ok := payload["embeds"]
	if !ok || len(rawEmbeds) == 0 || strings.TrimSpace(string(rawEmbeds)) == "null" {
		return nil
	}
	var rawList []map[string]any
	if err := json.Unmarshal(rawEmbeds, &rawList); err != nil {
		return output.UsageError("payload embeds must be a JSON array of embed objects")
	}
	if len(rawList) == 0 {
		return nil
	}

	embeds := make([]disgodiscord.Embed, len(rawList))
	modified := false

	for embedIndex, rawEmbed := range rawList {
		if rawEmbed == nil {
			return output.UsageError(fmt.Sprintf("embed %d must be a JSON object", embedIndex+1))
		}
		var embed disgodiscord.Embed
		if title, exists := rawEmbed["title"].(string); exists {
			embed.Title = title
		}
		if description, exists := rawEmbed["description"].(string); exists {
			embed.Description = description
		}
		if rawFooter, exists := rawEmbed["footer"].(map[string]any); exists {
			if text, hasText := rawFooter["text"].(string); hasText {
				embed.Footer = &disgodiscord.EmbedFooter{Text: text}
			}
		}
		if rawAuthor, exists := rawEmbed["author"].(map[string]any); exists {
			if name, hasName := rawAuthor["name"].(string); hasName {
				embed.Author = &disgodiscord.EmbedAuthor{Name: name}
			}
		}
		if rawFieldsValue, exists := rawEmbed["fields"]; exists && rawFieldsValue != nil {
			rawFields, isSlice := rawFieldsValue.([]any)
			if !isSlice {
				return output.UsageError(fmt.Sprintf("embed %d fields must be a JSON array", embedIndex+1))
			}
			embed.Fields = make([]disgodiscord.EmbedField, 0, len(rawFields))
			for fieldIndex, item := range rawFields {
				rawField, isMap := item.(map[string]any)
				if !isMap {
					return output.UsageError(fmt.Sprintf("embed %d field %d must be a JSON object", embedIndex+1, fieldIndex+1))
				}
				name, _ := rawField["name"].(string)
				value, _ := rawField["value"].(string)
				if options.EscapeFields {
					escapedName := EscapeMarkdown(name)
					escapedValue := EscapeMarkdown(value)
					if escapedName != name || escapedValue != value {
						rawField["name"] = escapedName
						rawField["value"] = escapedValue
						modified = true
					}
					name = escapedName
					value = escapedValue
				}
				embed.Fields = append(embed.Fields, disgodiscord.EmbedField{
					Name:  name,
					Value: value,
				})
			}
		}
		embeds[embedIndex] = embed
	}

	if options.Truncate {
		embeds = TruncateEmbeds(embeds)
		if len(rawList) > len(embeds) {
			rawList = rawList[:len(embeds)]
		}
		for embedIndex, embed := range embeds {
			rawEmbed := rawList[embedIndex]
			if _, exists := rawEmbed["title"].(string); exists {
				rawEmbed["title"] = embed.Title
			}
			if _, exists := rawEmbed["description"].(string); exists {
				rawEmbed["description"] = embed.Description
			}
			if rawFooter, exists := rawEmbed["footer"].(map[string]any); exists && embed.Footer != nil {
				if _, hasText := rawFooter["text"].(string); hasText {
					rawFooter["text"] = embed.Footer.Text
				}
			}
			if rawAuthor, exists := rawEmbed["author"].(map[string]any); exists && embed.Author != nil {
				if _, hasName := rawAuthor["name"].(string); hasName {
					rawAuthor["name"] = embed.Author.Name
				}
			}
			if rawFieldsValue, exists := rawEmbed["fields"]; exists && rawFieldsValue != nil {
				rawFields := rawFieldsValue.([]any)
				if len(rawFields) > len(embed.Fields) {
					rawFields = rawFields[:len(embed.Fields)]
				}
				for fieldIndex, field := range embed.Fields {
					rawField := rawFields[fieldIndex].(map[string]any)
					rawField["name"] = field.Name
					rawField["value"] = field.Value
				}
				rawEmbed["fields"] = rawFields
			}
		}
		modified = true
	}

	if err := ValidateEmbeds(embeds); err != nil {
		return err
	}

	if modified {
		encoded, err := json.Marshal(rawList)
		if err != nil {
			return fmt.Errorf("encode prepared embeds: %w", err)
		}
		payload["embeds"] = encoded
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
