package interact

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
)

func TestRouteCustomIDAndModalBuilder(t *testing.T) {
	assert.Equal(t, "modal", RouteCustomID("modal:feedback"))
	assert.Equal(t, "workflow", RouteCustomID("wf:1:step1:yes"))
	assert.Equal(t, "dashboard", RouteCustomID("dash:status:next"))
	assert.Equal(t, "voice", RouteCustomID("voice:mute"))
	assert.Equal(t, "", RouteCustomID("custom:button"))

	field, err := ParseModalFieldFlag("Notes:paragraph:optional")
	require.NoError(t, err)
	assert.Equal(t, "Notes", field.Label)
	assert.Equal(t, "paragraph", field.Style)
	require.NotNil(t, field.Required)
	assert.False(t, *field.Required)

	modal, err := BuildModal("Feedback", "feedback_form", []ModalFieldSpec{field})
	require.NoError(t, err)
	assert.Equal(t, "modal:feedback_form", modal.CustomID)
	assert.Equal(t, "Feedback", modal.Title)
	assert.Len(t, modal.Components, 1)
}

func TestDashboardValidationRejectsMixedLayouts(t *testing.T) {
	mixed := DashboardDefinition{
		DashboardID: "status",
		Pages: []DashboardPage{
			{Layout: "embed", Embed: map[string]any{"title": "Page 1"}},
			{Layout: "v2", Blocks: []V2BlockSpec{{Type: "text", Content: "Page 2"}}},
		},
	}
	err := mixed.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mixes embed and v2")

	validV2 := DashboardDefinition{
		DashboardID: "status",
		Pages: []DashboardPage{
			{
				Layout: "v2",
				Blocks: []V2BlockSpec{
					{
						Type:   "container",
						Accent: "5865F2",
						Children: []V2BlockSpec{
							{Type: "text", Content: "# System Status"},
							{Type: "separator", Spacing: "large"},
							{
								Type:    "section",
								Content: "All checks green",
								Button:  &voxdiscord.ComponentSpec{Label: "Refresh", CustomID: "refresh"},
							},
							{
								Type:  "gallery",
								Items: json.RawMessage(`["https://example.com/chart.png"]`),
							},
							{
								Type:  "buttons",
								Items: json.RawMessage(`[{"label":"Restart","style":"danger","custom_id":"restart"}]`),
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, validV2.Validate())
	assert.True(t, validV2.IsV2())

	nav := navButtons("status", 0, 2)
	built, err := BuildV2Components(validV2.Pages[0].Blocks, "status", nav)
	require.NoError(t, err)
	assert.Len(t, built, 2) // 1 container + 1 nav action row
}
