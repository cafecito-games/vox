package serverspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffPartialManagementAndPositionsIgnored(t *testing.T) {
	actual := Spec{
		Version: SpecVersion,
		Server: &ServerSettings{
			Name:              new("Cafecito"),
			Description:       new("Original"),
			VerificationLevel: new("low"),
		},
		Roles: []RoleSpec{
			{
				Name:        "Mod",
				Color:       new("ff0000"),
				Hoist:       new(true),
				Mentionable: new(false),
				Permissions: []string{"kick_members", "ban_members"},
				Position:    new(5),
			},
			{
				Name:  "LegacyRole",
				Color: new("000000"),
			},
		},
		Categories: []CategorySpec{
			{Name: "General", Position: new(0)},
		},
		Channels: []ChannelSpec{
			{
				Name:     "announcements",
				Type:     new("text"),
				Category: new("General"),
				Topic:    new("Old topic"),
				Slowmode: new(5),
				NSFW:     new(false),
				Position: new(1),
			},
		},
	}

	// Desired spec intentionally omits Description, Hoist, Mentionable, Slowmode,
	// and changes Position (which must NEVER trigger a diff).
	desired := Spec{
		Version: SpecVersion,
		Server: &ServerSettings{
			Name: new("Cafecito"),
		},
		Roles: []RoleSpec{
			{
				Name:     "Mod",
				Color:    new("#FF0000"),
				Position: new(99),
			},
			{
				Name:  "NewRole",
				Color: new("00ff00"),
			},
		},
		Categories: []CategorySpec{
			{Name: "General"},
		},
		Channels: []ChannelSpec{
			{
				Name:  "announcements",
				Topic: new("New topic"),
			},
		},
	}

	diff, err := Diff(actual, desired, "Cafecito")
	require.NoError(t, err)

	assert.Equal(t, 1, diff.Counts.Create)
	assert.Equal(t, 1, diff.Counts.Update)
	assert.Equal(t, 1, diff.Counts.Extra)
	assert.Equal(t, 3, diff.Counts.Unchanged) // server, Mod role, General category

	rendered := RenderChanges(diff.Changes, false, false)
	assert.Contains(t, rendered, "+ role NewRole")
	assert.Contains(t, rendered, "~ channel announcements (topic)")
	assert.Contains(t, rendered, "! role LegacyRole (not in spec, kept)")

	renderedPrune := RenderChanges(diff.Changes, true, false)
	assert.Contains(t, renderedPrune, "- role LegacyRole (DELETE)")
}

func TestLoadAndMarshalYAMLAndJSON(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{
		Version: SpecVersion,
		Roles:   []RoleSpec{{Name: "Admin", Color: new("5865f2"), Permissions: []string{"administrator"}}},
	}

	for _, name := range []string{"server.yaml", "server.json"} {
		filePath := filepath.Join(dir, name)
		encoded, err := Marshal(spec, filePath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filePath, encoded, 0o600))

		loaded, err := LoadFile(filePath)
		require.NoError(t, err)
		assert.Equal(t, SpecVersion, loaded.Version)
		require.Len(t, loaded.Roles, 1)
		assert.Equal(t, "Admin", loaded.Roles[0].Name)
	}
}
