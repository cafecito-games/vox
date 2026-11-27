package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/output"
)

func TestBotInviteOffline(t *testing.T) {
	_, stdout, _, err := newTestApp(t, "--profile", "readonly", "--json", "bot", "invite",
		"--application-id", "123", "--guild-id", "456", "--bundle", "voice", "--permissions", "send_messages")
	require.NoError(t, err)
	var result struct {
		URL             string   `json:"url"`
		PermissionNames []string `json:"permission_names"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.ElementsMatch(t, []string{"connect", "speak", "move_members", "send_messages"}, result.PermissionNames)
	invite, err := url.Parse(result.URL)
	require.NoError(t, err)
	assert.Equal(t, "discord.com", invite.Host)
	assert.Equal(t, "123", invite.Query().Get("client_id"))
	assert.Equal(t, "456", invite.Query().Get("guild_id"))
	assert.Equal(t, "true", invite.Query().Get("disable_guild_select"))
	assert.Equal(t, "bot applications.commands", invite.Query().Get("scope"))

	_, _, _, err = newTestApp(t, "--profile", "readonly", "bot", "application", "edit", "--message-content")
	require.Error(t, err)
	assert.Equal(t, output.ExitPermissionDenied, output.ExitCodeFor(err))
}

func TestBotSettingsREST(t *testing.T) {
	const application = `{"id":"123","name":"Releases","description":"Publishes releases",
		"verify_key":"omitted-from-vox-output","flags":8425472,
		"integration_types_config":{"0":{"oauth2_install_params":{"scopes":["bot","applications.commands"],"permissions":"1024"}},
		"1":{"oauth2_install_params":{"scopes":["applications.commands"],"permissions":"0"}}}}`
	const user = `{"id":"123","username":"ReleaseBot","bot":true,"email":"omitted-from-vox-output"}`
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bot test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /applications/@me":
			_, _ = w.Write([]byte(application))
		case "GET /users/@me":
			_, _ = w.Write([]byte(user))
		case "PATCH /users/@me":
			var patch map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			assert.Equal(t, map[string]any{"username": "ReleaseBot", "avatar": nil}, patch)
			_, _ = w.Write([]byte(user))
		case "PATCH /applications/@me":
			var patch map[string]json.RawMessage
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			flags := disgodiscord.ApplicationFlagApplicationCommandBadge |
				disgodiscord.ApplicationFlagGatewayPresence |
				disgodiscord.ApplicationFlagGatewayGuildMemberLimited | disgodiscord.ApplicationFlagGatewayMessageContentLimited
			wantFlags, err := json.Marshal(flags)
			assert.NoError(t, err)
			assert.JSONEq(t, string(wantFlags), string(patch["flags"]))
			assert.JSONEq(t, `""`, string(patch["description"]))
			assert.JSONEq(t, `[]`, string(patch["tags"]))
			assert.JSONEq(t, `null`, string(patch["icon"]))
			assert.JSONEq(t, `{"0":{"oauth2_install_params":{"scopes":["bot","applications.commands"],"permissions":"2048"}},
				"1":{"oauth2_install_params":{"scopes":["applications.commands"],"permissions":"0"}}}`, string(patch["integration_types_config"]))
			assert.Len(t, patch, 5)
			_, _ = w.Write([]byte(application))
		default:
			t.Errorf("unexpected Discord request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(handler.Close)
	dir := t.TempDir()
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		app := &App{
			Stdout: &stdout,
			Stderr: &stderr,
			newRest: func(token string, logger *slog.Logger) rest.Rest {
				return rest.New(rest.NewClient(token, rest.WithURL(handler.URL), rest.WithLogger(logger)))
			},
		}
		root := NewRootCommand(app)
		root.SetArgs(append([]string{"--config-dir", dir, "--profile", "full", "--token", "test-token", "--json", "bot"}, args...))
		err := root.Execute()
		return stdout.String(), err
	}

	result, err := run("show")
	require.NoError(t, err)
	assert.Contains(t, result, `"username": "ReleaseBot"`)
	assert.Contains(t, result, `"server-members": "limited"`)
	assert.NotContains(t, result, "omitted-from-vox-output")

	_, err = run("profile", "edit", "--username", "ReleaseBot", "--avatar=")
	require.NoError(t, err)
	_, err = run("application", "edit", "--description=", "--tags=", "--icon=",
		"--message-content", "--guild-install-permissions", "send_messages")
	require.NoError(t, err)
	_, err = run("application", "edit", "--server-members")
	require.NoError(t, err)
	_, err = run("application", "edit", "--presence=false")
	require.ErrorContains(t, err, "disable the approved presence intent in the Discord Developer Portal")
}
