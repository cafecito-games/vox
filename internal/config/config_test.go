package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreLoadSaveAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vox-home")
	store, err := NewStore(dir)
	require.NoError(t, err)

	empty, err := store.Load()
	require.NoError(t, err)
	assert.Empty(t, empty)

	require.NoError(t, store.Save(map[string]any{
		KeyToken: "secret-bot-token-123456",
		"guild":  "cafecito",
	}))

	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, "secret-bot-token-123456", loaded[KeyToken])
	assert.Equal(t, "cafecito", loaded["guild"])

	// Subsequent save merges rather than overwriting unrelated keys.
	require.NoError(t, store.Save(map[string]any{"theme": "dark"}))
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, "secret-bot-token-123456", loaded[KeyToken])
	assert.Equal(t, "dark", loaded["theme"])

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

		fileInfo, err := os.Stat(store.ConfigPath())
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	}
}

func TestResolveTokenPrecedence(t *testing.T) {
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	t.Setenv(EnvDiscordBotToken, "")
	t.Setenv(EnvDiscordToken, "")

	_, _, err = store.ResolveToken("")
	require.ErrorIs(t, err, ErrNoToken)

	// 4. DISCORD_TOKEN fallback.
	t.Setenv(EnvDiscordToken, "fallback-env-token")
	token, source, err := store.ResolveToken("")
	require.NoError(t, err)
	assert.Equal(t, "fallback-env-token", token)
	assert.Equal(t, TokenSourceFallbackEnv, source)

	// 3. Config file beats DISCORD_TOKEN.
	require.NoError(t, store.Save(map[string]any{KeyToken: "config-file-token"}))
	token, source, err = store.ResolveToken("")
	require.NoError(t, err)
	assert.Equal(t, "config-file-token", token)
	assert.Equal(t, TokenSourceConfig, source)

	// 2. DISCORD_BOT_TOKEN beats config file.
	t.Setenv(EnvDiscordBotToken, "bot-env-token")
	token, source, err = store.ResolveToken("")
	require.NoError(t, err)
	assert.Equal(t, "bot-env-token", token)
	assert.Equal(t, TokenSourceBotEnv, source)

	// 1. CLI flag beats everything.
	token, source, err = store.ResolveToken("cli-flag-token")
	require.NoError(t, err)
	assert.Equal(t, "cli-flag-token", token)
	assert.Equal(t, TokenSourceFlag, source)
}

func TestMaskToken(t *testing.T) {
	assert.Equal(t, "short", MaskToken("short"))
	assert.Equal(t, "12345678", MaskToken("12345678"))
	assert.Equal(t, "12345678...", MaskToken("1234567890abcdef"))
}

func TestDefaultDirHonorsEnv(t *testing.T) {
	customDir := filepath.Join(t.TempDir(), "custom-vox")
	t.Setenv(EnvConfigDir, customDir)

	resolved, err := DefaultDir()
	require.NoError(t, err)
	assert.Equal(t, customDir, resolved)
}
