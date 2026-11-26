package discord

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/cafecito-games/vox/internal/output"
)

// EmbedFieldSpec describes one field inside a Discord embed.
type EmbedFieldSpec struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// EmbedSpec describes an embed constructed either from CLI flags or serve JSONL.
type EmbedSpec struct {
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Color       string           `json:"color,omitempty"`
	URL         string           `json:"url,omitempty"`
	Footer      any              `json:"footer,omitempty"`
	Image       string           `json:"image,omitempty"`
	Thumbnail   string           `json:"thumbnail,omitempty"`
	Author      any              `json:"author,omitempty"`
	Fields      []EmbedFieldSpec `json:"fields,omitempty"`
}

// IsEmpty reports whether every field of EmbedSpec is unset.
func (spec EmbedSpec) IsEmpty() bool {
	return spec.Title == "" &&
		spec.Description == "" &&
		spec.Color == "" &&
		spec.URL == "" &&
		spec.Footer == nil &&
		spec.Image == "" &&
		spec.Thumbnail == "" &&
		spec.Author == nil &&
		len(spec.Fields) == 0
}

// ParseHexColor converts a hex RGB string ("5865F2" or "#5865F2") into an int color.
func ParseHexColor(raw string) (int, error) {
	cleaned := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if cleaned == "" {
		return 0, output.UsageError("empty color value (use hex like 5865F2)")
	}
	parsed, err := strconv.ParseInt(cleaned, 16, 32)
	if err != nil || parsed < 0 || parsed > 0xFFFFFF {
		return 0, output.UsageError(fmt.Sprintf("invalid color %q (use hex like ff0000)", raw))
	}
	return int(parsed), nil
}

// ParseEmbedFieldFlag parses a CLI --embed-field value formatted as "Name::Value[::Inline]".
func ParseEmbedFieldFlag(raw string) (EmbedFieldSpec, error) {
	parts := strings.Split(raw, "::")
	if len(parts) < 2 {
		return EmbedFieldSpec{}, output.UsageError(fmt.Sprintf(
			"invalid --embed-field %q (expected format Name::Value or Name::Value::true)",
			raw,
		))
	}
	inline := false
	if len(parts) >= 3 {
		inline = strings.EqualFold(strings.TrimSpace(parts[2]), "true")
	}
	return EmbedFieldSpec{
		Name:   parts[0],
		Value:  parts[1],
		Inline: inline,
	}, nil
}

// BuildEmbed converts an EmbedSpec into a disgo discord.Embed.
func BuildEmbed(spec EmbedSpec) (disgodiscord.Embed, error) {
	embed := disgodiscord.Embed{
		Title:       spec.Title,
		Description: spec.Description,
		URL:         spec.URL,
	}
	if strings.TrimSpace(spec.Color) != "" {
		color, err := ParseHexColor(spec.Color)
		if err != nil {
			return disgodiscord.Embed{}, err
		}
		embed.Color = color
	}
	if spec.Footer != nil {
		switch footer := spec.Footer.(type) {
		case string:
			if footer != "" {
				embed.Footer = &disgodiscord.EmbedFooter{Text: footer}
			}
		case map[string]any:
			text, _ := footer["text"].(string)
			iconURL, _ := footer["icon_url"].(string)
			if text != "" {
				embed.Footer = &disgodiscord.EmbedFooter{Text: text, IconURL: iconURL}
			}
		}
	}
	if spec.Image != "" {
		embed.Image = &disgodiscord.EmbedResource{URL: spec.Image}
	}
	if spec.Thumbnail != "" {
		embed.Thumbnail = &disgodiscord.EmbedResource{URL: spec.Thumbnail}
	}
	if spec.Author != nil {
		switch author := spec.Author.(type) {
		case string:
			if author != "" {
				embed.Author = &disgodiscord.EmbedAuthor{Name: author}
			}
		case map[string]any:
			name, _ := author["name"].(string)
			authorURL, _ := author["url"].(string)
			iconURL, _ := author["icon_url"].(string)
			if name != "" {
				embed.Author = &disgodiscord.EmbedAuthor{
					Name:    name,
					URL:     authorURL,
					IconURL: iconURL,
				}
			}
		}
	}
	if len(spec.Fields) > 0 {
		embed.Fields = make([]disgodiscord.EmbedField, 0, len(spec.Fields))
		for _, field := range spec.Fields {
			inline := field.Inline
			embed.Fields = append(embed.Fields, disgodiscord.EmbedField{
				Name:   field.Name,
				Value:  field.Value,
				Inline: &inline,
			})
		}
	}
	return embed, nil
}

// SelectOptionSpec defines a single option inside a string select menu.
type SelectOptionSpec struct {
	Label       string `json:"label"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// ComponentSpec describes an interactive message component (button or select menu).
type ComponentSpec struct {
	Type        string             `json:"type"`
	Label       string             `json:"label,omitempty"`
	Style       string             `json:"style,omitempty"`
	URL         string             `json:"url,omitempty"`
	CustomID    string             `json:"custom_id,omitempty"`
	Emoji       string             `json:"emoji,omitempty"`
	Disabled    bool               `json:"disabled,omitempty"`
	Placeholder string             `json:"placeholder,omitempty"`
	MinValues   *int               `json:"min_values,omitempty"`
	MaxValues   int                `json:"max_values,omitempty"`
	Options     []SelectOptionSpec `json:"options,omitempty"`
}

// ParseButtonStyle maps a string style name onto disgo's ButtonStyle.
func ParseButtonStyle(style string) disgodiscord.ButtonStyle {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "secondary", "grey", "gray":
		return disgodiscord.ButtonStyleSecondary
	case "success", "green":
		return disgodiscord.ButtonStyleSuccess
	case "danger", "red":
		return disgodiscord.ButtonStyleDanger
	case "link":
		return disgodiscord.ButtonStyleLink
	default:
		return disgodiscord.ButtonStylePrimary
	}
}

// BuildComponent converts a single ComponentSpec into a disgo InteractiveComponent.
func BuildComponent(spec ComponentSpec, customIDPrefix string) (disgodiscord.InteractiveComponent, error) {
	componentType := strings.ToLower(strings.TrimSpace(spec.Type))
	if componentType == "" {
		componentType = "button"
	}

	resolveCustomID := func(fallback string) string {
		id := spec.CustomID
		if id == "" {
			id = fallback
		}
		if id == "" {
			id = uuid.NewString()[:8]
		}
		if customIDPrefix != "" && !strings.HasPrefix(id, customIDPrefix) {
			return customIDPrefix + id
		}
		return id
	}

	switch componentType {
	case "button":
		style := ParseButtonStyle(spec.Style)
		label := spec.Label
		var button disgodiscord.ButtonComponent
		if style == disgodiscord.ButtonStyleLink {
			if spec.URL == "" {
				return nil, output.UsageError("link button requires a url field")
			}
			if label == "" {
				label = "Open"
			}
			button = disgodiscord.NewLinkButton(label, spec.URL)
		} else {
			if label == "" {
				label = "Button"
			}
			button = disgodiscord.NewButton(style, label, resolveCustomID(label), "", 0)
		}
		if spec.Disabled {
			button = button.AsDisabled()
		}
		if spec.Emoji != "" {
			button = button.WithEmoji(disgodiscord.ComponentEmoji{Name: spec.Emoji})
		}
		return button, nil

	case "select", "string_select":
		options := make([]disgodiscord.StringSelectMenuOption, 0, len(spec.Options))
		for _, opt := range spec.Options {
			value := opt.Value
			if value == "" {
				value = opt.Label
			}
			menuOpt := disgodiscord.NewStringSelectMenuOption(opt.Label, value).WithDefault(opt.Default)
			if opt.Description != "" {
				menuOpt = menuOpt.WithDescription(opt.Description)
			}
			if opt.Emoji != "" {
				menuOpt = menuOpt.WithEmoji(disgodiscord.ComponentEmoji{Name: opt.Emoji})
			}
			options = append(options, menuOpt)
		}
		menu := disgodiscord.NewStringSelectMenu(resolveCustomID("select"), spec.Placeholder, options...)
		if spec.MinValues != nil {
			menu = menu.WithMinValues(*spec.MinValues)
		}
		if spec.MaxValues > 0 {
			menu = menu.WithMaxValues(spec.MaxValues)
		}
		if spec.Disabled {
			menu = menu.AsDisabled()
		}
		return menu, nil

	case "user_select":
		menu := disgodiscord.NewUserSelectMenu(resolveCustomID("user_select"), spec.Placeholder)
		if spec.MinValues != nil {
			menu = menu.WithMinValues(*spec.MinValues)
		}
		if spec.MaxValues > 0 {
			menu = menu.WithMaxValues(spec.MaxValues)
		}
		return menu, nil

	case "role_select":
		menu := disgodiscord.NewRoleSelectMenu(resolveCustomID("role_select"), spec.Placeholder)
		if spec.MinValues != nil {
			menu = menu.WithMinValues(*spec.MinValues)
		}
		if spec.MaxValues > 0 {
			menu = menu.WithMaxValues(spec.MaxValues)
		}
		return menu, nil

	case "channel_select":
		menu := disgodiscord.NewChannelSelectMenu(resolveCustomID("channel_select"), spec.Placeholder)
		if spec.MinValues != nil {
			menu = menu.WithMinValues(*spec.MinValues)
		}
		if spec.MaxValues > 0 {
			menu = menu.WithMaxValues(spec.MaxValues)
		}
		return menu, nil

	default:
		return nil, output.UsageError(fmt.Sprintf("unsupported component type %q", spec.Type))
	}
}

// BuildComponentsFromRaw decodes the flexible "components" payload accepted by
// serve mode (either a flat list of component specs or a list of action rows)
// into disgo ActionRowComponents.
func BuildComponentsFromRaw(raw json.RawMessage) ([]disgodiscord.LayoutComponent, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rows [][]ComponentSpec
	if err := json.Unmarshal(raw, &rows); err == nil {
		return buildActionRows(rows)
	}
	var flat []ComponentSpec
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, output.UsageError(fmt.Sprintf("invalid components payload: %v", err))
	}
	return buildActionRows([][]ComponentSpec{flat})
}

func buildActionRows(rows [][]ComponentSpec) ([]disgodiscord.LayoutComponent, error) {
	components := make([]disgodiscord.LayoutComponent, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		items := make([]disgodiscord.InteractiveComponent, 0, len(row))
		for _, spec := range row {
			item, err := BuildComponent(spec, "")
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		components = append(components, disgodiscord.NewActionRow(items...))
	}
	return components, nil
}

// OpenFiles opens local file paths as Discord attachments and returns a cleanup
// callback that closes every opened file handle.
func OpenFiles(paths []string) ([]*disgodiscord.File, func(), error) {
	if len(paths) == 0 {
		return nil, func() {}, nil
	}
	handles := make([]*os.File, 0, len(paths))
	files := make([]*disgodiscord.File, 0, len(paths))
	cleanup := func() {
		for _, handle := range handles {
			_ = handle.Close()
		}
	}

	for _, filePath := range paths {
		handle, err := os.Open(filePath)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("open attachment %s: %w", filePath, err)
		}
		handles = append(handles, handle)
		files = append(files, disgodiscord.NewFile(filepath.Base(filePath), "", handle))
	}
	return files, cleanup, nil
}
