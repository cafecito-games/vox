package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/disgo/rest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
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

func TestWebhookPostBestEffortMode(t *testing.T) {
	t.Run("empty webhook URL exits 0 with warning", func(t *testing.T) {
		t.Setenv(webhookURLEnv, "")
		stdout, stderr, err := runWebhookPostTest(t, "http://127.0.0.1:1", "", "--best-effort", "Release published")
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":null,"delivered":false,"reason":"webhook_unconfigured"}`, stdout)
		assert.Contains(t, stderr, "DISCORD_WEBHOOK_URL is not configured")
	})

	t.Run("malformed webhook URL still returns usage error", func(t *testing.T) {
		t.Setenv(webhookURLEnv, "https://evil.example.test/api/webhooks/123/secret")
		_, _, err := runWebhookPostTest(t, "http://127.0.0.1:1", "", "--best-effort", "Release published")
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
	})

	t.Run("HTTP delivery error warns and exits 0", func(t *testing.T) {
		t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/secret")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":0,"message":"internal error"}`))
		}))
		t.Cleanup(server.Close)

		stdout, stderr, err := runWebhookPostTest(t, server.URL, "", "--best-effort", "Disk check")
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":null,"delivered":false,"reason":"webhook_post_failed"}`, stdout)
		assert.Contains(t, stderr, "warning:")
	})

	t.Run("timeout warns and reports webhook_timeout", func(t *testing.T) {
		t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/secret")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-req.Context().Done():
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		var stdout, stderr bytes.Buffer
		app := &App{
			In:     strings.NewReader(""),
			Stdout: &stdout,
			Stderr: &stderr,
			newRest: func(botToken string, logger *slog.Logger) rest.Rest {
				return rest.New(rest.NewClient(botToken,
					rest.WithURL(server.URL),
					rest.WithLogger(logger),
					rest.WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}),
				))
			},
		}
		root := NewRootCommand(app)
		root.SetArgs([]string{"--profile", "full", "--config-dir", t.TempDir(), "--json", "webhook", "post", "--best-effort", "Alert"})
		err := root.ExecuteContext(context.Background())
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":null,"delivered":false,"reason":"webhook_timeout"}`, stdout.String())
		assert.Contains(t, stderr.String(), "warning:")
	})

	t.Run("successful delivery marks delivered true", func(t *testing.T) {
		t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/secret")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"789","channel_id":"456"}`))
		}))
		t.Cleanup(server.Close)

		stdout, stderr, err := runWebhookPostTest(t, server.URL, "", "--best-effort", "All clear")
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"789","channel_id":"456","delivered":true}`, stdout)
		assert.Empty(t, stderr)
	})
}

func TestWebhookPostMultiPayloadBatchAndPartialFailure(t *testing.T) {
	t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/test-webhook")

	cases := []struct {
		name  string
		stdin string
	}{
		{
			name:  "JSON array",
			stdin: `[{"content":"first"},{"content":"second"},{"content":"third"}]`,
		},
		{
			name:  "JSONL stream",
			stdin: "{\"content\":\"first\"}\n{\"content\":\"second\"}\n{\"content\":\"third\"}\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var received []string
			var nextID atomic.Int64
			nextID.Store(100)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				received = append(received, body["content"].(string))
				id := nextID.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"%d","channel_id":"456"}`, id)
			}))
			t.Cleanup(server.Close)

			var clientCreates atomic.Int32
			stdout, stderr, err := runWebhookPostTestWithArgs(t, server.URL, testCase.stdin, &clientCreates, true, "--payload-file", "-")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Equal(t, int32(1), clientCreates.Load(), "batch must reuse one rest.Client")
			assert.Equal(t, []string{"first", "second", "third"}, received)
			assert.JSONEq(t, `[
				{"id":"101","channel_id":"456"},
				{"id":"102","channel_id":"456"},
				{"id":"103","channel_id":"456"}
			]`, stdout)
		})
	}

	t.Run("partial failure stops on first error and reports delivered count", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			count := attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if count == 3 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":0,"message":"boom"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"%d","channel_id":"456"}`, 200+count)
		}))
		t.Cleanup(server.Close)

		batch := `[{"content":"1"},{"content":"2"},{"content":"3"},{"content":"4"}]`
		_, _, err := runWebhookPostTest(t, server.URL, batch, "--payload-file", "-")
		require.Error(t, err)
		assert.Equal(t, int32(3), attempts.Load(), "must stop at first failure without posting item 4")
		assert.Contains(t, err.Error(), "Delivered 2 of 4 Discord webhook messages")

		var cliErr *output.CLIError
		require.ErrorAs(t, err, &cliErr)
		assert.Equal(t, 2, cliErr.Details["sent"])
		assert.Equal(t, 4, cliErr.Details["total"])

		// Same partial failure under --best-effort exits 0 and reports sent/total/messages.
		attempts.Store(0)
		stdout, stderr, err := runWebhookPostTest(t, server.URL, batch, "--best-effort", "--payload-file", "-")
		require.NoError(t, err)
		assert.Equal(t, int32(3), attempts.Load())
		assert.Contains(t, stderr, "Delivered 2 of 4 Discord webhook messages")
		assert.JSONEq(t, `{
			"delivered": false,
			"sent": 2,
			"total": 4,
			"reason": "webhook_post_failed",
			"messages": [
				{"id":"201","channel_id":"456"},
				{"id":"202","channel_id":"456"}
			]
		}`, stdout)
	})

	t.Run("preflight embed validation fails before sending any message in batch", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1","channel_id":"456"}`))
		}))
		t.Cleanup(server.Close)

		batch := `[{"content":"ok"},{"embeds":[{"fields":[{"name":"disk /var","value":"` + strings.Repeat("x", 1200) + `"}]}]}]`
		_, _, err := runWebhookPostTest(t, server.URL, batch, "--payload-file", "-")
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
		assert.Equal(t, int32(0), requests.Load(), "no request should be sent when any batch payload fails validation")
		assert.Contains(t, err.Error(), `payload 2: embed field 1 "disk /var" value is 1200 characters (limit 1024)`)
	})

	t.Run("rejects empty array and multi-payload with files", func(t *testing.T) {
		_, _, err := runWebhookPostTest(t, "http://127.0.0.1:1", `[]`, "--payload-file", "-")
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))

		attachment := filepath.Join(t.TempDir(), "a.txt")
		require.NoError(t, os.WriteFile(attachment, []byte("hi"), 0o600))
		_, _, err = runWebhookPostTest(t, "http://127.0.0.1:1", `[{"content":"1"},{"content":"2"}]`, "--payload-file", "-", "--file", attachment)
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
		assert.Contains(t, err.Error(), "--file cannot be combined with multi-payload --payload-file")
	})
}

func TestWebhookPostEmbedValidationTruncateAndEscape(t *testing.T) {
	t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/test-webhook")
	oversizeFields := `[{"name":"disk_/var*","value":"` + strings.Repeat("*x*", 400) + `"}]`

	t.Run("rejects oversize embed before request", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
		}))
		t.Cleanup(server.Close)

		_, _, err := runWebhookPostTest(t, server.URL, "", "--embed-title", "Disk", "--embed-fields-json", oversizeFields)
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
		assert.Equal(t, int32(0), requests.Load())
		assert.Contains(t, err.Error(), `embed field 1 "disk_/var*" value is 1200 characters (limit 1024)`)
	})

	t.Run("truncate and escape-fields send valid escaped payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var payload struct {
				Embeds []struct {
					Fields []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"fields"`
				} `json:"embeds"`
			}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
			require.Len(t, payload.Embeds, 1)
			require.Len(t, payload.Embeds[0].Fields, 1)
			field := payload.Embeds[0].Fields[0]
			assert.Equal(t, `disk\_/var\*`, field.Name)
			assert.Equal(t, voxdiscord.MaxEmbedFieldValueLength, utf8.RuneCountInString(field.Value))
			assert.True(t, strings.HasPrefix(field.Value, `\*x\*`))
			assert.True(t, strings.HasSuffix(field.Value, "…"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"789","channel_id":"456"}`))
		}))
		t.Cleanup(server.Close)

		_, _, err := runWebhookPostTest(t, server.URL, "",
			"--embed-title", "Disk",
			"--embed-fields-json", oversizeFields,
			"--escape-fields",
			"--truncate",
		)
		require.NoError(t, err)
	})
}

func TestWebhookPostOutputModes(t *testing.T) {
	t.Setenv(webhookURLEnv, "https://discord.com/api/webhooks/123/test-webhook")
	var nextID atomic.Int64
	nextID.Store(500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		id := nextID.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"%d","channel_id":"456"}`, id)
	}))
	t.Cleanup(server.Close)

	t.Run("output id single and batch", func(t *testing.T) {
		stdout, stderr, err := runWebhookPostTestWithArgs(t, server.URL, "", nil, false, "--output", "id", "Hello")
		require.NoError(t, err)
		assert.Equal(t, "501\n", stdout)
		assert.Empty(t, stderr)

		stdout, stderr, err = runWebhookPostTestWithArgs(t, server.URL, `[{"content":"a"},{"content":"b"}]`, nil, false, "--output", "id", "--payload-file", "-")
		require.NoError(t, err)
		assert.Equal(t, "502\n503\n", stdout)
		assert.Empty(t, stderr)
	})

	t.Run("output github-output single, batch, and unconfigured", func(t *testing.T) {
		stdout, stderr, err := runWebhookPostTestWithArgs(t, server.URL, "", nil, false, "--output", "github-output", "Hello")
		require.NoError(t, err)
		assert.Equal(t, "message-id=504\nmessage-ids=[\"504\"]\ndelivered=true\n", stdout)
		assert.Empty(t, stderr)

		stdout, stderr, err = runWebhookPostTestWithArgs(t, server.URL, `[{"content":"a"},{"content":"b"}]`, nil, false, "--output", "github-output", "--payload-file", "-")
		require.NoError(t, err)
		assert.Equal(t, "message-id=\nmessage-ids=[\"505\",\"506\"]\ndelivered=true\n", stdout)
		assert.Empty(t, stderr)

		t.Setenv(webhookURLEnv, "")
		stdout, stderr, err = runWebhookPostTestWithArgs(t, server.URL, "", nil, false, "--best-effort", "--output", "github-output", "Hello")
		require.NoError(t, err)
		assert.Equal(t, "message-id=\nmessage-ids=[]\ndelivered=false\n", stdout)
		assert.Contains(t, stderr, "DISCORD_WEBHOOK_URL is not configured")
	})

	t.Run("rejects invalid output mode or combination with --json", func(t *testing.T) {
		_, _, err := runWebhookPostTestWithArgs(t, server.URL, "", nil, true, "--output", "id", "Hello")
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))

		_, _, err = runWebhookPostTestWithArgs(t, server.URL, "", nil, false, "--output", "yaml", "Hello")
		require.Error(t, err)
		assert.Equal(t, output.ExitUsage, output.ExitCodeFor(err))
	})
}

func runWebhookPostTest(t *testing.T, apiURL, stdin string, args ...string) (string, string, error) {
	t.Helper()
	return runWebhookPostTestWithArgs(t, apiURL, stdin, nil, true, args...)
}

func runWebhookPostTestWithArgs(t *testing.T, apiURL, stdin string, clientCreates *atomic.Int32, useJSON bool, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := &App{
		In:     strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
		newRest: func(botToken string, logger *slog.Logger) rest.Rest {
			assert.Empty(t, botToken)
			if clientCreates != nil {
				clientCreates.Add(1)
			}
			return rest.New(rest.NewClient(botToken, rest.WithURL(apiURL), rest.WithLogger(logger)))
		},
	}
	root := NewRootCommand(app)
	baseArgs := []string{"--profile", "full", "--config-dir", t.TempDir()}
	if useJSON {
		baseArgs = append(baseArgs, "--json")
	}
	baseArgs = append(baseArgs, "webhook", "post")
	root.SetArgs(append(baseArgs, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}
