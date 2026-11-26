package doctor

import (
	"errors"
	"testing"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/config"
)

func TestRunOfflineDiagnostics(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	t.Setenv(config.EnvDiscordBotToken, "")
	t.Setenv(config.EnvDiscordToken, "")

	// Missing token causes exactly 1 failure; optional checks are skipped.
	report := Run(Options{
		Store:    store,
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Getenv:   func(string) string { return "" },
	})
	assert.Equal(t, 1, report.Failures)
	assert.GreaterOrEqual(t, report.Skipped, 6)
	text := FormatText(report)
	assert.Contains(t, text, "[FAIL] bot token")
	assert.Contains(t, text, "pure-Go DAVE E2EE")

	// Configuring a token brings failures down to 0.
	require.NoError(t, store.Save(map[string]any{config.KeyToken: "test-token"}))
	reportOK := Run(Options{
		Store:    store,
		LookPath: func(string) (string, error) { return "/usr/bin/ffmpeg", nil },
		Getenv:   func(string) string { return "" },
	})
	assert.Equal(t, 0, reportOK.Failures)
	assert.Contains(t, FormatText(reportOK), "OK — no problems found")
}

func TestEvaluateGuildPermissionsDetects2026Splits(t *testing.T) {
	checks := EvaluateGuildPermissions("Cafecito", "123", disgodiscord.PermissionManageMessages)
	require.Len(t, checks, 3)
	assert.Equal(t, "permission splits", checks[1].Name)
	assert.False(t, checks[1].OK)
	assert.Contains(t, checks[1].Detail, "pin_messages")
	assert.Contains(t, checks[1].Detail, "bypass_slowmode")

	adminChecks := EvaluateGuildPermissions("Cafecito", "123", disgodiscord.PermissionAdministrator)
	require.Len(t, adminChecks, 2)
	assert.Equal(t, "administrator", adminChecks[1].Name)
	assert.True(t, adminChecks[1].OK)
}
