package setup

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/config"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
)

func TestSetupRefusesNonInteractiveOrJSON(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)

	var stderr bytes.Buffer
	err = Run(Options{
		Store:       store,
		Security:    security.NewManager(store),
		Interactive: false,
		Err:         &stderr,
	})
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "vox config set token")

	stderr.Reset()
	err = Run(Options{
		Store:       store,
		Security:    security.NewManager(store),
		Interactive: true,
		UseJSON:     true,
		Err:         &stderr,
	})
	require.Error(t, err)
}

func TestSetupInteractiveFlow(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	sec := security.NewManager(store)

	t.Setenv(config.EnvDiscordBotToken, "")
	t.Setenv(config.EnvDiscordToken, "")

	// Inputs:
	// 1. messaging=y, moderation=n, voice=n, admin=n
	// 2. profile=chat
	// 3. voice=n
	input := strings.Join([]string{
		"y", "n", "n", "n",
		"chat",
		"n",
	}, "\n") + "\n"

	var stdout, stderr bytes.Buffer
	err = Run(Options{
		Store:       store,
		Security:    sec,
		Interactive: true,
		In:          strings.NewReader(input),
		Out:         &stdout,
		Err:         &stderr,
		ReadSecret:  func(string) (string, error) { return "valid-secret-token", nil },
		ValidateToken: func(token string) (BotIdentity, error) {
			assert.Equal(t, "valid-secret-token", token)
			return BotIdentity{
				ID:     snowflake.ID(999111),
				Name:   "VoxTestBot",
				Guilds: []string{"Cafecito HQ"},
			}, nil
		},
	})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "Authenticated as VoxTestBot")
	assert.Contains(t, stdout.String(), "client_id=999111")
	assert.Equal(t, "chat", sec.ActiveProfileName())

	cfg, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, "valid-secret-token", cfg[config.KeyToken])
}

func TestSetupCancelsImmediatelyOnContextDone(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)

	t.Setenv(config.EnvDiscordBotToken, "")
	t.Setenv(config.EnvDiscordToken, "")

	pipeReader, pipeWriter := io.Pipe()
	defer func() {
		_ = pipeWriter.Close()
		_ = pipeReader.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(Options{
			Context:     ctx,
			Store:       store,
			Security:    security.NewManager(store),
			Interactive: true,
			In:          pipeReader,
			Out:         &stdout,
			Err:         &stderr,
		})
	}()

	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case runErr := <-done:
		require.Error(t, runErr)
		assert.ErrorIs(t, runErr, context.Canceled)
		assert.Equal(t, output.ExitInterrupted, output.ExitCodeFor(runErr))
	case <-time.After(time.Second):
		t.Fatal("setup.Run did not cancel promptly when context was cancelled")
	}
}
