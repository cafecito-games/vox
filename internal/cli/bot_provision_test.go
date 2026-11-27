package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/config"
	"github.com/cafecito-games/vox/internal/output"
)

func TestBotProvisionResumesAndVerifiesPermissions(t *testing.T) {
	const token = "provision-test-token"
	t.Setenv(config.EnvDiscordBotToken, "")
	t.Setenv(config.EnvDiscordToken, "")
	dir := t.TempDir()
	spec := filepath.Join(dir, "bot.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(`version: 1
name: release-bot
guild_id: "456"
permissions: [view_channel, send_messages]
profile:
  username: ReleaseBot
application:
  description: Publishes releases
  guild_install_permissions: [view_channel]
  intents:
    message_content: true
`), 0o600))

	var mutex sync.Mutex
	var applicationEdits, profileEdits int
	var joined bool
	livePermissions := "1024"
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bot wrong-token" {
			assert.Equal(t, "GET /applications/@me", r.Method+" "+r.URL.Path)
			_, _ = w.Write([]byte(`{"id":"789","name":"OtherBot"}`))
			return
		}
		assert.Equal(t, "Bot "+token, r.Header.Get("Authorization"))
		writeApplication := func() {
			if applicationEdits == 0 {
				_, _ = w.Write([]byte(`{"id":"123","name":"Releases"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"123","name":"Releases","description":"Publishes releases","flags":524288,
				"install_params":{"permissions":"1024","scopes":["bot","applications.commands"]}}`))
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /applications/@me":
			writeApplication()
		case "PATCH /applications/@me":
			applicationEdits++
			var patch map[string]json.RawMessage
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			assert.JSONEq(t, `"Publishes releases"`, string(patch["description"]))
			assert.JSONEq(t, `524288`, string(patch["flags"]))
			assert.JSONEq(t, `{"permissions":"1024","scopes":["bot","applications.commands"]}`, string(patch["install_params"]))
			writeApplication()
		case "GET /users/@me":
			username := "Initial"
			if profileEdits > 1 {
				username = "ReleaseBot"
			}
			_, _ = fmt.Fprintf(w, `{"id":"123","username":%q,"bot":true}`, username)
		case "PATCH /users/@me":
			profileEdits++
			var patch map[string]string
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			assert.Equal(t, map[string]string{"username": "ReleaseBot"}, patch)
			if profileEdits == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":50035,"message":"Profile update failed"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"123","username":"ReleaseBot","bot":true}`))
		case "GET /users/@me/guilds":
			if joined {
				_, _ = w.Write([]byte(`[{"id":"456","name":"Test Server"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "GET /guilds/456/permissions/member/123":
			assert.True(t, joined)
			_, _ = fmt.Fprintf(w, `{"permissions":%q}`, livePermissions)
		default:
			t.Errorf("unexpected Discord request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(handler.Close)
	run := func(args ...string) (botProvisionResult, error) {
		var stdout, stderr bytes.Buffer
		app := &App{
			Stdout: &stdout,
			Stderr: &stderr,
			newRest: func(secret string, logger *slog.Logger) rest.Rest {
				return rest.New(rest.NewClient(secret, rest.WithURL(handler.URL), rest.WithLogger(logger)))
			},
		}
		root := NewRootCommand(app)
		root.SetArgs(append([]string{"--config-dir", dir, "--profile", "full", "--json", "bot", "provision", spec}, args...))
		err := root.Execute()
		assert.NotContains(t, stdout.String()+stderr.String(), token)
		var result botProvisionResult
		if err == nil {
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
		}
		return result, err
	}

	result, err := run()
	require.NoError(t, err)
	assert.Equal(t, "needs_discord_app", result.Status)
	assert.Equal(t, discordApplicationsURL, result.PortalURL)

	result, err = run("--application-id", "123")
	require.NoError(t, err)
	assert.Equal(t, "needs_bot_token", result.Status)
	assert.Equal(t, discordApplicationsURL+"/123/bot", result.PortalURL)

	_, err = run("--application-id", "123", "--token", "wrong-token")
	var cliErr *output.CLIError
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, "application_mismatch", cliErr.Code)
	binding := filepath.Join(dir, "bot-provisions", "bot-release-bot", "application_id")
	assert.NoFileExists(t, binding)

	t.Setenv(config.EnvDiscordBotToken, token)
	_, err = run("--application-id", "123")
	require.ErrorContains(t, err, "Profile update failed")

	result, err = run()
	require.NoError(t, err)
	assert.Equal(t, "needs_install_approval", result.Status)
	invite, err := url.Parse(result.InstallURL)
	require.NoError(t, err)
	assert.Equal(t, "123", invite.Query().Get("client_id"))
	assert.Equal(t, "456", invite.Query().Get("guild_id"))
	assert.Equal(t, "3072", invite.Query().Get("permissions"))

	mutex.Lock()
	joined = true
	mutex.Unlock()
	result, err = run()
	require.NoError(t, err)
	assert.Equal(t, "needs_permissions", result.Status)
	assert.Equal(t, []string{"send_messages"}, result.MissingPermissions)

	mutex.Lock()
	livePermissions = "8"
	mutex.Unlock()
	result, err = run()
	require.NoError(t, err)
	assert.Equal(t, "ready", result.Status)

	_, err = run("--application-id", "789")
	require.ErrorContains(t, err, "already bound to application_id 123")

	mutex.Lock()
	defer mutex.Unlock()
	assert.Equal(t, 1, applicationEdits)
	assert.Equal(t, 2, profileEdits)
}

func TestBotProvisionBindingIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application_id")
	errs := make(chan error, 2)
	for _, id := range []snowflake.ID{123, 456} {
		go func() { errs <- claimProvisionID(path, id) }()
	}
	first, second := <-errs, <-errs
	if first == nil {
		first, second = second, first
	}
	require.NoError(t, second)
	require.ErrorContains(t, first, "already bound")
	id, err := readProvisionID(path)
	require.NoError(t, err)
	assert.Contains(t, []snowflake.ID{123, 456}, id)
}
