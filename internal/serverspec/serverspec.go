// Package serverspec implements declarative Discord server-as-code management
// (`vox server export`, `vox server diff`, `vox server apply`).
//
// Specs support both YAML (.yaml/.yml) and JSON formats. Items are matched by
// name for cross-server portability, omitted fields are left unmanaged, and
// positions are exported for reference but never applied.
package serverspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"go.yaml.in/yaml/v3"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

// SpecVersion is the schema version emitted and accepted by serverspec.
const SpecVersion = 1

// Spec is the top-level declarative representation of a Discord server.
type Spec struct {
	Version    int             `json:"version" yaml:"version"`
	Server     *ServerSettings `json:"server,omitempty" yaml:"server,omitempty"`
	Roles      []RoleSpec      `json:"roles,omitempty" yaml:"roles,omitempty"`
	Categories []CategorySpec  `json:"categories,omitempty" yaml:"categories,omitempty"`
	Channels   []ChannelSpec   `json:"channels,omitempty" yaml:"channels,omitempty"`
}

// ServerSettings captures declarative guild-level metadata.
type ServerSettings struct {
	Name              *string `json:"name,omitempty" yaml:"name,omitempty"`
	Description       *string `json:"description,omitempty" yaml:"description,omitempty"`
	VerificationLevel *string `json:"verification_level,omitempty" yaml:"verification_level,omitempty"`
}

// RoleSpec describes a non-managed, non-@everyone Discord role.
type RoleSpec struct {
	Name        string  `json:"name" yaml:"name"`
	Color       *string `json:"color,omitempty" yaml:"color,omitempty"`
	Hoist       *bool   `json:"hoist,omitempty" yaml:"hoist,omitempty"`
	Mentionable *bool   `json:"mentionable,omitempty" yaml:"mentionable,omitempty"`
	Permissions any     `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Position    *int    `json:"position,omitempty" yaml:"position,omitempty"`
}

// CategorySpec describes a channel category.
type CategorySpec struct {
	Name     string `json:"name" yaml:"name"`
	Position *int   `json:"position,omitempty" yaml:"position,omitempty"`
}

// OverwriteSpec describes a role-scoped channel permission overwrite.
type OverwriteSpec struct {
	Role  string   `json:"role" yaml:"role"`
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}

// ChannelSpec describes a guild text, voice, forum, or stage channel.
type ChannelSpec struct {
	Name       string          `json:"name" yaml:"name"`
	Type       *string         `json:"type,omitempty" yaml:"type,omitempty"`
	Category   *string         `json:"category,omitempty" yaml:"category,omitempty"`
	Topic      *string         `json:"topic,omitempty" yaml:"topic,omitempty"`
	Slowmode   *int            `json:"slowmode,omitempty" yaml:"slowmode,omitempty"`
	NSFW       *bool           `json:"nsfw,omitempty" yaml:"nsfw,omitempty"`
	Position   *int            `json:"position,omitempty" yaml:"position,omitempty"`
	Overwrites []OverwriteSpec `json:"overwrites,omitempty" yaml:"overwrites,omitempty"`
}

// ChangeOp classifies a single diff entry.
type ChangeOp string

const (
	// OpCreate marks a resource present in the spec but missing in the server.
	OpCreate ChangeOp = "create"
	// OpUpdate marks an existing resource whose managed fields differ from the spec.
	OpUpdate ChangeOp = "update"
	// OpUnchanged marks an existing resource matching all managed fields in the spec.
	OpUnchanged ChangeOp = "unchanged"
	// OpExtra marks a resource present on the server but absent from the spec.
	OpExtra ChangeOp = "extra"
)

// FieldDiff captures the before/after values for one changed field.
type FieldDiff struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// Change describes one diff action on a server resource.
type Change struct {
	Op     ChangeOp    `json:"op"`
	Kind   string      `json:"kind"`
	Name   string      `json:"name"`
	Fields []string    `json:"fields,omitempty"`
	Diffs  []FieldDiff `json:"-"`
}

// DiffCounts summarizes how many resources fall into each operation bucket.
type DiffCounts struct {
	Create    int `json:"create"`
	Update    int `json:"update"`
	Extra     int `json:"extra"`
	Unchanged int `json:"unchanged"`
}

// DiffResult holds the full comparison between an actual server and a desired spec.
type DiffResult struct {
	Server  string     `json:"server"`
	Counts  DiffCounts `json:"counts"`
	Changes []Change   `json:"changes"`
}

// VerificationLevelName converts a disgo VerificationLevel to its CLI string name.
func VerificationLevelName(level disgodiscord.VerificationLevel) string {
	switch level {
	case disgodiscord.VerificationLevelNone:
		return "none"
	case disgodiscord.VerificationLevelLow:
		return "low"
	case disgodiscord.VerificationLevelMedium:
		return "medium"
	case disgodiscord.VerificationLevelHigh:
		return "high"
	case disgodiscord.VerificationLevelVeryHigh:
		return "highest"
	default:
		return "none"
	}
}

// ParseVerificationLevel converts a string name into a disgo VerificationLevel.
func ParseVerificationLevel(name string) (disgodiscord.VerificationLevel, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "none":
		return disgodiscord.VerificationLevelNone, nil
	case "low":
		return disgodiscord.VerificationLevelLow, nil
	case "medium":
		return disgodiscord.VerificationLevelMedium, nil
	case "high":
		return disgodiscord.VerificationLevelHigh, nil
	case "highest", "very_high":
		return disgodiscord.VerificationLevelVeryHigh, nil
	default:
		return 0, output.UsageError(fmt.Sprintf("invalid verification level %q (use none, low, medium, high, highest)", name))
	}
}

// ChannelTypeName maps a disgo ChannelType into a portable spec type name.
func ChannelTypeName(channelType disgodiscord.ChannelType) string {
	switch channelType {
	case disgodiscord.ChannelTypeGuildText:
		return "text"
	case disgodiscord.ChannelTypeGuildVoice:
		return "voice"
	case disgodiscord.ChannelTypeGuildCategory:
		return "category"
	case disgodiscord.ChannelTypeGuildNews:
		return "news"
	case disgodiscord.ChannelTypeGuildStageVoice:
		return "stage"
	case disgodiscord.ChannelTypeGuildForum:
		return "forum"
	case disgodiscord.ChannelTypeGuildMedia:
		return "media"
	default:
		return fmt.Sprintf("type_%d", channelType)
	}
}

// LoadFile reads and validates a JSON or YAML server specification from disk.
func LoadFile(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read spec %s: %w", path, err)
	}

	var spec Spec
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		if err := yaml.Unmarshal(raw, &spec); err != nil {
			return Spec{}, output.UsageError(fmt.Sprintf("parse YAML spec %s: %v", path, err))
		}
	} else {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return Spec{}, output.UsageError(fmt.Sprintf("parse JSON spec %s: %v", path, err))
		}
	}

	if spec.Version != 0 && spec.Version != SpecVersion {
		return Spec{}, output.UsageError(fmt.Sprintf("unsupported spec version %d (expected %d)", spec.Version, SpecVersion))
	}
	if spec.Version == 0 {
		spec.Version = SpecVersion
	}
	return spec, nil
}

// Marshal encodes spec as YAML (for .yaml/.yml) or indented JSON otherwise.
func Marshal(spec Spec, path string) ([]byte, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		return yaml.Marshal(spec)
	}
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// Export fetches live guild roles and channels via REST and builds a portable Spec.
func Export(restClient rest.Rest, guild *disgodiscord.RestGuild) (Spec, error) {
	roles, err := restClient.GetRoles(guild.ID)
	if err != nil {
		return Spec{}, voxdiscord.MapError(err)
	}
	channels, err := restClient.GetGuildChannels(guild.ID)
	if err != nil {
		return Spec{}, voxdiscord.MapError(err)
	}
	return BuildSpec(guild, roles, channels), nil
}

// BuildSpec constructs a Spec from fetched guild, role, and channel snapshots.
func BuildSpec(guild *disgodiscord.RestGuild, roles []disgodiscord.Role, channels []disgodiscord.GuildChannel) Spec {
	name := guild.Name
	verification := VerificationLevelName(guild.VerificationLevel)
	spec := Spec{
		Version: SpecVersion,
		Server: &ServerSettings{
			Name:              &name,
			Description:       guild.Description,
			VerificationLevel: &verification,
		},
		Roles:      make([]RoleSpec, 0),
		Categories: make([]CategorySpec, 0),
		Channels:   make([]ChannelSpec, 0),
	}

	roleNamesByID := make(map[snowflake.ID]string, len(roles))
	sortedRoles := slices.Clone(roles)
	slices.SortFunc(sortedRoles, func(a, b disgodiscord.Role) int {
		if a.Position != b.Position {
			return b.Position - a.Position
		}
		return strings.Compare(a.Name, b.Name)
	})

	for _, role := range sortedRoles {
		roleNamesByID[role.ID] = role.Name
		if role.ID == guild.ID || role.Name == "@everyone" || role.Managed {
			continue
		}
		colorHex := fmt.Sprintf("%06x", role.Color)
		hoist := role.Hoist
		mentionable := role.Mentionable
		position := role.Position
		spec.Roles = append(spec.Roles, RoleSpec{
			Name:        role.Name,
			Color:       &colorHex,
			Hoist:       &hoist,
			Mentionable: &mentionable,
			Permissions: voxdiscord.PermissionNames(role.Permissions),
			Position:    &position,
		})
	}

	categoriesByID := make(map[snowflake.ID]string)
	var categoryChannels []disgodiscord.GuildChannel
	var regularChannels []disgodiscord.GuildChannel

	for _, channel := range channels {
		if channel.Type() == disgodiscord.ChannelTypeGuildCategory {
			categoriesByID[channel.ID()] = channel.Name()
			categoryChannels = append(categoryChannels, channel)
		} else {
			regularChannels = append(regularChannels, channel)
		}
	}

	slices.SortFunc(categoryChannels, func(a, b disgodiscord.GuildChannel) int {
		if a.Position() != b.Position() {
			return a.Position() - b.Position()
		}
		return strings.Compare(a.Name(), b.Name())
	})
	for _, cat := range categoryChannels {
		pos := cat.Position()
		spec.Categories = append(spec.Categories, CategorySpec{
			Name:     cat.Name(),
			Position: &pos,
		})
	}

	slices.SortFunc(regularChannels, func(a, b disgodiscord.GuildChannel) int {
		if a.Position() != b.Position() {
			return a.Position() - b.Position()
		}
		return strings.Compare(a.Name(), b.Name())
	})
	for _, channel := range regularChannels {
		channelType := ChannelTypeName(channel.Type())
		pos := channel.Position()
		entry := ChannelSpec{
			Name:     channel.Name(),
			Type:     &channelType,
			Position: &pos,
		}
		if parentID := channel.ParentID(); parentID != nil {
			if parentName, ok := categoriesByID[*parentID]; ok {
				catName := parentName
				entry.Category = &catName
			}
		}

		switch typed := channel.(type) {
		case disgodiscord.GuildTextChannel:
			entry.Topic = typed.Topic()
			slowmode := typed.RateLimitPerUser()
			nsfw := typed.NSFW()
			entry.Slowmode = &slowmode
			entry.NSFW = &nsfw
		case disgodiscord.GuildVoiceChannel:
			slowmode := typed.RateLimitPerUser()
			nsfw := typed.NSFW()
			entry.Slowmode = &slowmode
			entry.NSFW = &nsfw
		case disgodiscord.GuildForumChannel:
			entry.Topic = typed.Topic
			slowmode := typed.RateLimitPerUser
			nsfw := typed.NSFW
			entry.Slowmode = &slowmode
			entry.NSFW = &nsfw
		case disgodiscord.GuildNewsChannel:
			entry.Topic = typed.Topic()
			nsfw := typed.NSFW()
			entry.NSFW = &nsfw
		}

		overwrites := make([]OverwriteSpec, 0)
		for _, overwrite := range channel.PermissionOverwrites() {
			roleOverwrite, ok := overwrite.(disgodiscord.RolePermissionOverwrite)
			if !ok {
				continue
			}
			roleName, ok := roleNamesByID[roleOverwrite.RoleID]
			if !ok {
				continue
			}
			overwrites = append(overwrites, OverwriteSpec{
				Role:  roleName,
				Allow: voxdiscord.PermissionNames(roleOverwrite.Allow),
				Deny:  voxdiscord.PermissionNames(roleOverwrite.Deny),
			})
		}
		slices.SortFunc(overwrites, func(a, b OverwriteSpec) int {
			return strings.Compare(a.Role, b.Role)
		})
		if len(overwrites) > 0 {
			entry.Overwrites = overwrites
		}
		spec.Channels = append(spec.Channels, entry)
	}

	return spec
}

// Diff compares actual server state against desired spec using partial-field
// semantics (omitted desired fields are treated as unmanaged).
func Diff(actual, desired Spec, serverName string) (DiffResult, error) {
	result := DiffResult{
		Server:  serverName,
		Changes: make([]Change, 0),
	}

	record := func(change Change) {
		switch change.Op {
		case OpCreate:
			result.Counts.Create++
		case OpUpdate:
			result.Counts.Update++
		case OpExtra:
			result.Counts.Extra++
		case OpUnchanged:
			result.Counts.Unchanged++
		}
		result.Changes = append(result.Changes, change)
	}

	// 1. Server-level settings.
	if desired.Server != nil && actual.Server != nil {
		var diffs []FieldDiff
		if desired.Server.Name != nil && !ptrStringEqual(actual.Server.Name, desired.Server.Name) {
			diffs = append(diffs, FieldDiff{Field: "name", Before: derefString(actual.Server.Name), After: *desired.Server.Name})
		}
		if desired.Server.Description != nil && !ptrStringEqual(actual.Server.Description, desired.Server.Description) {
			diffs = append(diffs, FieldDiff{Field: "description", Before: derefString(actual.Server.Description), After: *desired.Server.Description})
		}
		if desired.Server.VerificationLevel != nil && !ptrStringEqual(actual.Server.VerificationLevel, desired.Server.VerificationLevel) {
			diffs = append(diffs, FieldDiff{Field: "verification_level", Before: derefString(actual.Server.VerificationLevel), After: *desired.Server.VerificationLevel})
		}
		if len(diffs) > 0 {
			record(Change{Op: OpUpdate, Kind: "server", Name: serverName, Fields: fieldNames(diffs), Diffs: diffs})
		} else {
			record(Change{Op: OpUnchanged, Kind: "server", Name: serverName})
		}
	}

	// 2. Roles.
	actualRoles := make(map[string]RoleSpec, len(actual.Roles))
	for _, role := range actual.Roles {
		actualRoles[role.Name] = role
	}
	desiredRoleNames := make(map[string]struct{}, len(desired.Roles))
	for _, want := range desired.Roles {
		desiredRoleNames[want.Name] = struct{}{}
		have, exists := actualRoles[want.Name]
		if !exists {
			record(Change{Op: OpCreate, Kind: "role", Name: want.Name})
			continue
		}
		diffs, err := diffRole(have, want)
		if err != nil {
			return DiffResult{}, err
		}
		if len(diffs) > 0 {
			record(Change{Op: OpUpdate, Kind: "role", Name: want.Name, Fields: fieldNames(diffs), Diffs: diffs})
		} else {
			record(Change{Op: OpUnchanged, Kind: "role", Name: want.Name})
		}
	}
	for _, have := range actual.Roles {
		if _, ok := desiredRoleNames[have.Name]; !ok {
			record(Change{Op: OpExtra, Kind: "role", Name: have.Name})
		}
	}

	// 3. Categories.
	actualCategories := make(map[string]CategorySpec, len(actual.Categories))
	for _, cat := range actual.Categories {
		actualCategories[cat.Name] = cat
	}
	desiredCatNames := make(map[string]struct{}, len(desired.Categories))
	for _, want := range desired.Categories {
		desiredCatNames[want.Name] = struct{}{}
		if _, exists := actualCategories[want.Name]; !exists {
			record(Change{Op: OpCreate, Kind: "category", Name: want.Name})
		} else {
			record(Change{Op: OpUnchanged, Kind: "category", Name: want.Name})
		}
	}
	for _, have := range actual.Categories {
		if _, ok := desiredCatNames[have.Name]; !ok {
			record(Change{Op: OpExtra, Kind: "category", Name: have.Name})
		}
	}

	// 4. Channels.
	actualChannels := make(map[string]ChannelSpec, len(actual.Channels))
	for _, channel := range actual.Channels {
		actualChannels[channel.Name] = channel
	}
	desiredChanNames := make(map[string]struct{}, len(desired.Channels))
	for _, want := range desired.Channels {
		desiredChanNames[want.Name] = struct{}{}
		have, exists := actualChannels[want.Name]
		if !exists {
			record(Change{Op: OpCreate, Kind: "channel", Name: want.Name})
			continue
		}
		diffs := diffChannel(have, want)
		if len(diffs) > 0 {
			record(Change{Op: OpUpdate, Kind: "channel", Name: want.Name, Fields: fieldNames(diffs), Diffs: diffs})
		} else {
			record(Change{Op: OpUnchanged, Kind: "channel", Name: want.Name})
		}
	}
	for _, have := range actual.Channels {
		if _, ok := desiredChanNames[have.Name]; !ok {
			record(Change{Op: OpExtra, Kind: "channel", Name: have.Name})
		}
	}

	return result, nil
}

// RenderChanges formats a DiffResult for human-readable terminal display.
func RenderChanges(changes []Change, prune, showUnchanged bool) string {
	var lines []string
	for _, change := range changes {
		if change.Op == OpUnchanged && !showUnchanged {
			continue
		}
		switch change.Op {
		case OpCreate:
			lines = append(lines, fmt.Sprintf("  + %s %s", change.Kind, change.Name))
		case OpUpdate:
			lines = append(lines, fmt.Sprintf("  ~ %s %s (%s)", change.Kind, change.Name, strings.Join(change.Fields, ", ")))
		case OpUnchanged:
			lines = append(lines, fmt.Sprintf("    %s %s", change.Kind, change.Name))
		case OpExtra:
			if prune {
				lines = append(lines, fmt.Sprintf("  - %s %s (DELETE)", change.Kind, change.Name))
			} else {
				lines = append(lines, fmt.Sprintf("  ! %s %s (not in spec, kept)", change.Kind, change.Name))
			}
		}
	}
	if len(lines) == 0 {
		return "No differences."
	}
	return strings.Join(lines, "\n")
}

// ApplyOptions controls `vox server apply` execution.
type ApplyOptions struct {
	DryRun       bool
	Prune        bool
	Reason       string
	ConfirmPrune func(extras []Change) error
}

// ApplyResult summarizes what `vox server apply` changed or left untouched.
type ApplyResult struct {
	Server    string     `json:"server"`
	DryRun    bool       `json:"dry_run,omitempty"`
	Counts    DiffCounts `json:"counts,omitzero"`
	Changes   []Change   `json:"changes,omitempty"`
	Applied   []string   `json:"applied,omitempty"`
	Pruned    []string   `json:"pruned,omitempty"`
	LeftAlone []string   `json:"left_alone,omitempty"`
}

// Apply reconciles a live Discord guild against desired spec.
//
// Apply is additive by default: resources missing from desired are kept unless
// options.Prune is true (in which case ConfirmPrune is invoked before deleting).
func Apply(restClient rest.Rest, guild *disgodiscord.RestGuild, desired Spec, options ApplyOptions) (ApplyResult, error) {
	actual, err := Export(restClient, guild)
	if err != nil {
		return ApplyResult{}, err
	}
	diff, err := Diff(actual, desired, guild.Name)
	if err != nil {
		return ApplyResult{}, err
	}

	if options.DryRun {
		return ApplyResult{
			Server:  guild.Name,
			DryRun:  true,
			Counts:  diff.Counts,
			Changes: diff.Changes,
		}, nil
	}

	var extras []Change
	for _, change := range diff.Changes {
		if change.Op == OpExtra {
			extras = append(extras, change)
		}
	}
	if options.Prune && len(extras) > 0 && options.ConfirmPrune != nil {
		if err := options.ConfirmPrune(extras); err != nil {
			return ApplyResult{}, err
		}
	}

	reqOpts := make([]rest.RequestOpt, 0, 1)
	if options.Reason != "" {
		reqOpts = append(reqOpts, rest.WithReason(options.Reason))
	}

	applied := make([]string, 0)
	pruned := make([]string, 0)
	leftAlone := make([]string, 0)

	// Apply server settings if changed.
	for _, change := range diff.Changes {
		if change.Kind == "server" && change.Op == OpUpdate && desired.Server != nil {
			update := disgodiscord.GuildUpdate{
				Name:        desired.Server.Name,
				Description: desired.Server.Description,
			}
			if desired.Server.VerificationLevel != nil {
				level, err := ParseVerificationLevel(*desired.Server.VerificationLevel)
				if err != nil {
					return ApplyResult{}, err
				}
				update.VerificationLevel = omit.New(&level)
			}
			if _, err := restClient.UpdateGuild(guild.ID, update, reqOpts...); err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
			applied = append(applied, "server "+guild.Name)
		}
	}

	// Re-fetch live roles and channels for accurate IDs.
	liveRoles, err := restClient.GetRoles(guild.ID)
	if err != nil {
		return ApplyResult{}, voxdiscord.MapError(err)
	}
	rolesByName := make(map[string]disgodiscord.Role, len(liveRoles))
	for _, role := range liveRoles {
		rolesByName[role.Name] = role
	}

	// 1. Roles first so channel overwrites can reference newly created roles.
	desiredRolesByName := make(map[string]RoleSpec, len(desired.Roles))
	for _, role := range desired.Roles {
		desiredRolesByName[role.Name] = role
	}
	for _, change := range diff.Changes {
		if change.Kind != "role" || (change.Op != OpCreate && change.Op != OpUpdate) {
			continue
		}
		want := desiredRolesByName[change.Name]
		if change.Op == OpCreate {
			create := disgodiscord.RoleCreate{Name: want.Name}
			if want.Color != nil {
				color, err := voxdiscord.ParseHexColor(*want.Color)
				if err != nil {
					return ApplyResult{}, err
				}
				create.Color = color
			}
			if want.Hoist != nil {
				create.Hoist = *want.Hoist
			}
			if want.Mentionable != nil {
				create.Mentionable = *want.Mentionable
			}
			if want.Permissions != nil {
				perms, err := ParseSpecPermissions(want.Permissions)
				if err != nil {
					return ApplyResult{}, err
				}
				create.Permissions = &perms
			}
			created, err := restClient.CreateRole(guild.ID, create, reqOpts...)
			if err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
			rolesByName[created.Name] = *created
			applied = append(applied, "role "+want.Name)
		} else {
			existing := rolesByName[want.Name]
			update := disgodiscord.RoleUpdate{
				Hoist:       want.Hoist,
				Mentionable: want.Mentionable,
			}
			if want.Color != nil {
				color, err := voxdiscord.ParseHexColor(*want.Color)
				if err != nil {
					return ApplyResult{}, err
				}
				update.Color = &color
			}
			if want.Permissions != nil {
				perms, err := ParseSpecPermissions(want.Permissions)
				if err != nil {
					return ApplyResult{}, err
				}
				update.Permissions = &perms
			}
			updated, err := restClient.UpdateRole(guild.ID, existing.ID, update, reqOpts...)
			if err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
			rolesByName[updated.Name] = *updated
			applied = append(applied, "role "+want.Name)
		}
	}

	// 2. Categories second so channels can attach to new categories.
	liveChannels, err := restClient.GetGuildChannels(guild.ID)
	if err != nil {
		return ApplyResult{}, voxdiscord.MapError(err)
	}
	categoriesByName := make(map[string]snowflake.ID)
	channelsByName := make(map[string]disgodiscord.GuildChannel)
	for _, channel := range liveChannels {
		if channel.Type() == disgodiscord.ChannelTypeGuildCategory {
			categoriesByName[channel.Name()] = channel.ID()
		} else {
			channelsByName[channel.Name()] = channel
		}
	}

	for _, change := range diff.Changes {
		if change.Kind != "category" || change.Op != OpCreate {
			continue
		}
		created, err := restClient.CreateGuildChannel(guild.ID, disgodiscord.GuildCategoryChannelCreate{
			Name: change.Name,
		}, reqOpts...)
		if err != nil {
			return ApplyResult{}, voxdiscord.MapError(err)
		}
		categoriesByName[created.Name()] = created.ID()
		applied = append(applied, "category "+change.Name)
	}

	// 3. Channels and overwrites.
	desiredChannelsByName := make(map[string]ChannelSpec, len(desired.Channels))
	for _, ch := range desired.Channels {
		desiredChannelsByName[ch.Name] = ch
	}
	for _, change := range diff.Changes {
		if change.Kind != "channel" || (change.Op != OpCreate && change.Op != OpUpdate) {
			continue
		}
		want := desiredChannelsByName[change.Name]
		var parentID snowflake.ID
		if want.Category != nil && *want.Category != "" {
			var ok bool
			parentID, ok = categoriesByName[*want.Category]
			if !ok {
				return ApplyResult{}, output.NewError("not_found", fmt.Sprintf("category %q not found for channel %q", *want.Category, want.Name))
			}
		}

		var targetID snowflake.ID
		if change.Op == OpCreate {
			channelType := "text"
			if want.Type != nil && *want.Type != "" {
				channelType = strings.ToLower(*want.Type)
			}
			var payload disgodiscord.GuildChannelCreate
			switch channelType {
			case "text":
				c := disgodiscord.GuildTextChannelCreate{Name: want.Name, ParentID: parentID}
				if want.Topic != nil {
					c.Topic = *want.Topic
				}
				if want.NSFW != nil {
					c.NSFW = *want.NSFW
				}
				if want.Slowmode != nil {
					c.RateLimitPerUser = *want.Slowmode
				}
				payload = c
			case "voice":
				c := disgodiscord.GuildVoiceChannelCreate{Name: want.Name, ParentID: parentID}
				if want.NSFW != nil {
					c.NSFW = *want.NSFW
				}
				if want.Slowmode != nil {
					c.RateLimitPerUser = *want.Slowmode
				}
				payload = c
			case "forum":
				c := disgodiscord.GuildForumChannelCreate{Name: want.Name, ParentID: parentID}
				if want.Topic != nil {
					c.Topic = *want.Topic
				}
				if want.Slowmode != nil {
					c.RateLimitPerUser = *want.Slowmode
				}
				payload = c
			default:
				return ApplyResult{}, output.UsageError(fmt.Sprintf("unsupported channel type %q in spec for %q", channelType, want.Name))
			}
			created, err := restClient.CreateGuildChannel(guild.ID, payload, reqOpts...)
			if err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
			targetID = created.ID()
			applied = append(applied, "channel "+want.Name)
		} else {
			existing := channelsByName[want.Name]
			targetID = existing.ID()
			update := disgodiscord.GuildTextChannelUpdate{
				Topic:            want.Topic,
				NSFW:             want.NSFW,
				RateLimitPerUser: want.Slowmode,
			}
			if want.Category != nil {
				update.ParentID = &parentID
			}
			if _, err := restClient.UpdateChannel(existing.ID(), update, reqOpts...); err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
			applied = append(applied, "channel "+want.Name)
		}

		for _, overwrite := range want.Overwrites {
			role, ok := rolesByName[overwrite.Role]
			if !ok {
				return ApplyResult{}, output.NewError("not_found", fmt.Sprintf("role %q not found for overwrite on %q", overwrite.Role, want.Name))
			}
			allow, err := voxdiscord.ParsePermissionNames(overwrite.Allow)
			if err != nil {
				return ApplyResult{}, err
			}
			deny, err := voxdiscord.ParsePermissionNames(overwrite.Deny)
			if err != nil {
				return ApplyResult{}, err
			}
			if err := restClient.UpdatePermissionOverwrite(targetID, role.ID, disgodiscord.RolePermissionOverwriteUpdate{
				Allow: &allow,
				Deny:  &deny,
			}, reqOpts...); err != nil {
				return ApplyResult{}, voxdiscord.MapError(err)
			}
		}
	}

	// 4. Prune or report extras.
	for _, extra := range extras {
		label := extra.Kind + " " + extra.Name
		if !options.Prune {
			leftAlone = append(leftAlone, label)
			continue
		}
		switch extra.Kind {
		case "role":
			if role, ok := rolesByName[extra.Name]; ok {
				if err := restClient.DeleteRole(guild.ID, role.ID, reqOpts...); err != nil {
					return ApplyResult{}, voxdiscord.MapError(err)
				}
				pruned = append(pruned, label)
			}
		case "category":
			if catID, ok := categoriesByName[extra.Name]; ok {
				if err := restClient.DeleteChannel(catID, reqOpts...); err != nil {
					return ApplyResult{}, voxdiscord.MapError(err)
				}
				pruned = append(pruned, label)
			}
		case "channel":
			if ch, ok := channelsByName[extra.Name]; ok {
				if err := restClient.DeleteChannel(ch.ID(), reqOpts...); err != nil {
					return ApplyResult{}, voxdiscord.MapError(err)
				}
				pruned = append(pruned, label)
			}
		}
	}

	return ApplyResult{
		Server:    guild.Name,
		Applied:   applied,
		Pruned:    pruned,
		LeftAlone: leftAlone,
	}, nil
}

// ParseSpecPermissions accepts either a list of permission names or a raw numeric bitfield.
func ParseSpecPermissions(raw any) (disgodiscord.Permissions, error) {
	switch value := raw.(type) {
	case int:
		return disgodiscord.Permissions(value), nil
	case int64:
		return disgodiscord.Permissions(value), nil
	case float64:
		return disgodiscord.Permissions(int64(value)), nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return voxdiscord.ParsePermissionNames([]string{value})
		}
		return disgodiscord.Permissions(parsed), nil
	case []string:
		return voxdiscord.ParsePermissionNames(value)
	case []any:
		names := make([]string, 0, len(value))
		for _, item := range value {
			names = append(names, fmt.Sprint(item))
		}
		return voxdiscord.ParsePermissionNames(names)
	default:
		return 0, output.UsageError(fmt.Sprintf("unsupported permissions value %T", raw))
	}
}

func diffRole(have, want RoleSpec) ([]FieldDiff, error) {
	var diffs []FieldDiff
	if want.Color != nil {
		haveColor := strings.ToLower(strings.TrimPrefix(derefString(have.Color), "#"))
		wantColor := strings.ToLower(strings.TrimPrefix(*want.Color, "#"))
		if haveColor != wantColor {
			diffs = append(diffs, FieldDiff{Field: "color", Before: haveColor, After: wantColor})
		}
	}
	if want.Hoist != nil && derefBool(have.Hoist) != *want.Hoist {
		diffs = append(diffs, FieldDiff{Field: "hoist", Before: derefBool(have.Hoist), After: *want.Hoist})
	}
	if want.Mentionable != nil && derefBool(have.Mentionable) != *want.Mentionable {
		diffs = append(diffs, FieldDiff{Field: "mentionable", Before: derefBool(have.Mentionable), After: *want.Mentionable})
	}
	if want.Permissions != nil {
		haveMask, err := ParseSpecPermissions(have.Permissions)
		if err != nil {
			return nil, err
		}
		wantMask, err := ParseSpecPermissions(want.Permissions)
		if err != nil {
			return nil, err
		}
		if haveMask != wantMask {
			diffs = append(diffs, FieldDiff{
				Field:  "permissions",
				Before: voxdiscord.PermissionNames(haveMask),
				After:  voxdiscord.PermissionNames(wantMask),
			})
		}
	}
	return diffs, nil
}

func diffChannel(have, want ChannelSpec) []FieldDiff {
	var diffs []FieldDiff
	if want.Type != nil && !strings.EqualFold(derefString(have.Type), *want.Type) {
		diffs = append(diffs, FieldDiff{Field: "type", Before: derefString(have.Type), After: *want.Type})
	}
	if want.Category != nil && derefString(have.Category) != *want.Category {
		diffs = append(diffs, FieldDiff{Field: "category", Before: derefString(have.Category), After: *want.Category})
	}
	if want.Topic != nil && derefString(have.Topic) != *want.Topic {
		diffs = append(diffs, FieldDiff{Field: "topic", Before: derefString(have.Topic), After: *want.Topic})
	}
	if want.Slowmode != nil && derefInt(have.Slowmode) != *want.Slowmode {
		diffs = append(diffs, FieldDiff{Field: "slowmode", Before: derefInt(have.Slowmode), After: *want.Slowmode})
	}
	if want.NSFW != nil && derefBool(have.NSFW) != *want.NSFW {
		diffs = append(diffs, FieldDiff{Field: "nsfw", Before: derefBool(have.NSFW), After: *want.NSFW})
	}
	if want.Overwrites != nil {
		haveNorm := normalizeOverwrites(have.Overwrites)
		wantNorm := normalizeOverwrites(want.Overwrites)
		if !reflect.DeepEqual(haveNorm, wantNorm) {
			diffs = append(diffs, FieldDiff{Field: "overwrites", Before: haveNorm, After: wantNorm})
		}
	}
	return diffs
}

func normalizeOverwrites(overwrites []OverwriteSpec) []OverwriteSpec {
	out := make([]OverwriteSpec, len(overwrites))
	for i, overwrite := range overwrites {
		allow := slices.Clone(overwrite.Allow)
		deny := slices.Clone(overwrite.Deny)
		slices.Sort(allow)
		slices.Sort(deny)
		out[i] = OverwriteSpec{Role: overwrite.Role, Allow: allow, Deny: deny}
	}
	slices.SortFunc(out, func(a, b OverwriteSpec) int {
		return strings.Compare(a.Role, b.Role)
	})
	return out
}

func fieldNames(diffs []FieldDiff) []string {
	names := make([]string, len(diffs))
	for i, d := range diffs {
		names[i] = d.Field
	}
	slices.Sort(names)
	return names
}

func ptrStringEqual(a, b *string) bool {
	return derefString(a) == derefString(b)
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
