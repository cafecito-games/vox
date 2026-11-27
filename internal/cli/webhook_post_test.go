package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/rest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/output"
)

func TestWebhookPostUsesOnlyWebhookCredential(t *testing.T) {
	const secret = "webhook-secret"
	tests := []struct {
		name     string
		args     []string
		stdin    string
		wantJSON string
	}{
		{
			name: "status embed",
			args: []string{"--embed-title", "Beta uploaded", "--embed-url", "https://github.com/example/game/actions/runs/1",
				"--embed-desc", "Build 123", "--embed-color", "2ECC71", "--embed-fields-json", `[{"name":"Build","value":"123","inline":true}]`},
			wantJSON: `{"allowed_mentions":{"parse":[]},"embeds":[{"title":"Beta uploaded","url":"https://github.com/example/game/actions/runs/1",
				"description":"Build 123","color":3066993,"fields":[{"name":"Build","value":"123","inline":true}]}]}`,
		},
		{
			name:     "plain message with no mentions",
			args:     []string{"--", "-- @everyone"},
			wantJSON: `{"allowed_mentions":{"parse":[]},"content":"-- @everyone"}`,
		},
		{
			name:     "raw payload preserves fields and mentions",
			args:     []string{"--payload-file", "-"},
			stdin:    `{"content":"<@789>","allowed_mentions":{"parse":[],"users":["789"]},"username":"Beta builds"}`,
			wantJSON: `{"content":"<@789>","allowed_mentions":{"parse":[],"users":["789"]},"username":"Beta builds"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "/webhooks/123/"+secret, req.URL.Path)
				assert.Equal(t, "true", req.URL.Query().Get("wait"))
				assert.Equal(t, "456", req.URL.Query().Get("thread_id"))
				assert.Empty(t, req.Header.Get("Authorization"))
				var payload json.RawMessage
				if assert.NoError(t, json.NewDecoder(req.Body).Decode(&payload)) {
					assert.JSONEq(t, tc.wantJSON, string(payload))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"789","channel_id":"456"}`))
			}))
			t.Cleanup(server.Close)
			t.Setenv(webhookURLEnv, "https://discord.com/api/v10/webhooks/123/"+secret+"?thread_id=456&wait=false")
			stdout, stderr, err := runWebhookPostTest(t, server.URL, tc.stdin, tc.args...)
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"789","channel_id":"456"}`, stdout)
			assert.Empty(t, stderr)
		})
	}
}

func TestWebhookPostRedactsErrorsAndChecksProfiles(t *testing.T) {
	const secret = "sensitive-webhook-token"
	t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/"+secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":10015,"message":"Unknown webhook: ` + secret + `"}`))
	}))
	t.Cleanup(server.Close)
	stdout, stderr, err := runWebhookPostTest(t, server.URL, "", "Check", "--log-level", "debug")
	require.ErrorContains(t, err, "HTTP 404, Discord code 10015")
	assert.NotContains(t, stdout+stderr+err.Error(), secret)

	_, _, _, err = newTestApp(t, "--profile", "readonly", "webhook", "post", "Check")
	require.Error(t, err)
	assert.Equal(t, output.ExitPermissionDenied, output.ExitCodeFor(err))

	t.Setenv(webhookURLEnv, "https://discord.com.example.test/api/webhooks/123/"+secret)
	_, _, err = runWebhookPostTest(t, server.URL, "", "Check")
	require.ErrorContains(t, err, "must be an HTTPS Discord incoming webhook URL")
	assert.NotContains(t, err.Error(), secret)
}

func TestWebhookPostAttachesFilesToPayload(t *testing.T) {
	t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/test-webhook")
	dir := t.TempDir()
	attachment := filepath.Join(dir, "report.txt")
	payload := filepath.Join(dir, "payload.json")
	require.NoError(t, os.WriteFile(attachment, []byte("report body"), 0o600))
	require.NoError(t, os.WriteFile(payload, []byte(`{"content":"Build report"}`), 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if assert.NoError(t, req.ParseMultipartForm(1<<20)) {
			defer func() { _ = req.MultipartForm.RemoveAll() }()
			assert.JSONEq(t, `{"content":"Build report","allowed_mentions":{"parse":[]}}`, req.MultipartForm.Value["payload_json"][0])
			files := req.MultipartForm.File["files[0]"]
			if assert.Len(t, files, 1) {
				assert.Equal(t, "report.txt", files[0].Filename)
				assert.EqualValues(t, len("report body"), files[0].Size)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"789","channel_id":"456"}`))
	}))
	t.Cleanup(server.Close)
	_, _, err := runWebhookPostTest(t, server.URL, "", "--payload-file", payload, "--file", attachment)
	require.NoError(t, err)
}

func runWebhookPostTest(t *testing.T, apiURL, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := &App{
		In:     strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
		newRest: func(botToken string, logger *slog.Logger) rest.Rest {
			assert.Empty(t, botToken)
			return rest.New(rest.NewClient(botToken, rest.WithURL(apiURL), rest.WithLogger(logger)))
		},
	}
	root := NewRootCommand(app)
	root.SetArgs(append([]string{"--profile", "full", "--config-dir", t.TempDir(), "--json", "webhook", "post"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}
