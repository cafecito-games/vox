package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/config"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
)

func newTestApp(t *testing.T, args ...string) (*App, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	configDir := t.TempDir()
	return runTestAppWithDir(t, configDir, args...)
}

func runTestAppWithDir(t *testing.T, configDir string, args ...string) (*App, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	nonInteractive := false
	app := &App{
		In:          strings.NewReader(""),
		Stdout:      &stdout,
		Stderr:      &stderr,
		Interactive: &nonInteractive,
	}
	root := NewRootCommand(app)
	fullArgs := append([]string{"--config-dir", configDir}, args...)
	root.SetArgs(fullArgs)
	err := root.Execute()
	return app, &stdout, &stderr, err
}

func TestSchemaCommandExportsCompleteAgentMetadata(t *testing.T) {
	_, stdout, stderr, err := newTestApp(t, "--json", "schema")
	require.NoError(t, err)
	assert.Empty(t, stderr.String())

	var schema ToolSchema
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &schema))
	assert.Equal(t, "vox", schema.Name)
	assert.Equal(t, output.ExitPermissionDenied, schema.ExitCodes["permission_denied"])
	assert.Len(t, schema.ServeActions, 73)
	assert.GreaterOrEqual(t, len(schema.Commands), 25)
}

func TestConfigPermissionAuditAndScheduleFlows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvDiscordBotToken, "")
	t.Setenv(config.EnvDiscordToken, "")
	t.Setenv(security.EnvProfile, "")

	// Set & show config.
	_, stdout, _, err := runTestAppWithDir(t, dir, "config", "set", "token", "abcdefghijklmnop")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Set token.")

	_, stdout, _, err = runTestAppWithDir(t, dir, "config", "show")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "token: abcdefgh...")

	// Switch to readonly and verify restricted commands are rejected with exit code 3.
	_, stdout, _, err = runTestAppWithDir(t, dir, "permission", "set", "readonly")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "readonly")

	_, _, _, err = runTestAppWithDir(t, dir, "config", "set", "token", "hacked")
	require.Error(t, err)
	assert.Equal(t, output.ExitPermissionDenied, output.ExitCodeFor(err))

	// Override with --profile full works for a single invocation.
	_, stdout, _, err = runTestAppWithDir(t, dir, "--profile", "full", "schedule", "add", "standup", "--every", "5m", "--action", `message send "#general" "Standup time!"`)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Added schedule: standup")

	// Readonly can list schedules and view audit log.
	_, stdout, _, err = runTestAppWithDir(t, dir, "--json", "schedule", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"name": "standup"`)

	_, stdout, _, err = runTestAppWithDir(t, dir, "--json", "audit", "show")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"command": "schedule add"`)

	// Destructive schedule remove under --profile full with --yes succeeds.
	_, stdout, _, err = runTestAppWithDir(t, dir, "--profile", "full", "--yes", "schedule", "remove", "standup")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `Removed schedule "standup"`)
}

func TestSetupRefusesNonInteractive(t *testing.T) {
	_, _, stderr, err := newTestApp(t, "setup")
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "vox setup requires an interactive terminal")
}

func TestVersionFlagAndCommand(t *testing.T) {
	_, stdout, _, err := newTestApp(t, "--version")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "vox version ")

	_, stdout, _, err = newTestApp(t, "--json", "version")
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &decoded))
	assert.NotEmpty(t, decoded["version"])
	assert.NotEmpty(t, decoded["go_version"])
}

func TestRestrictedProfilesNeverGrantBareSensitiveGroups(t *testing.T) {
	app := NewApp()
	root := NewRootCommand(app)

	var leafPaths []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden || cmd.Name() == "help" || cmd.Name() == "completion" {
			return
		}
		if len(cmd.Commands()) == 0 {
			leafPaths = append(leafPaths, security.NormalizeCommandPath(cmd.CommandPath()))
			return
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	require.NotEmpty(t, leafPaths)

	profiles := security.DefaultProfiles()
	// Ensure chat cannot mutate config or server state.
	assert.False(t, security.EvaluateProfile(profiles["chat"], "config set"))
	assert.False(t, security.EvaluateProfile(profiles["chat"], "server apply"))
	assert.False(t, security.EvaluateProfile(profiles["chat"], "permission set"))

	// Ensure moderation cannot elevate its own profile or overwrite tokens.
	assert.False(t, security.EvaluateProfile(profiles["moderation"], "permission set"))
	assert.False(t, security.EvaluateProfile(profiles["moderation"], "config set"))
	assert.False(t, security.EvaluateProfile(profiles["moderation"], "server apply"))

	// Ensure readonly allows no mutating commands.
	for _, forbidden := range []string{
		"message send", "message delete", "channel create", "channel delete",
		"role create", "role delete", "member kick", "member ban",
		"config set", "permission set", "audit clear", "schedule add",
		"schedule remove", "server edit", "server apply", "automod create",
		"emoji upload", "invite create", "webhook create", "event create",
	} {
		assert.False(t, security.EvaluateProfile(profiles["readonly"], forbidden), "readonly must deny %q", forbidden)
	}
}
