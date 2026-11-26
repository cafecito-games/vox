package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTextAndJSONLoggers(t *testing.T) {
	t.Run("text format at debug level", func(t *testing.T) {
		var buffer bytes.Buffer
		logger, err := New(Options{
			Level:  "debug",
			Format: "text",
			Writer: &buffer,
		})
		require.NoError(t, err)

		logger.Debug("hello world", "key", "value")
		output := buffer.String()
		assert.Contains(t, output, "level=DEBUG")
		assert.Contains(t, output, "msg=\"hello world\"")
		assert.Contains(t, output, "key=value")
	})

	t.Run("json format respects level filtering", func(t *testing.T) {
		var buffer bytes.Buffer
		logger, err := New(Options{
			Level:  "warn",
			Format: "json",
			Writer: &buffer,
		})
		require.NoError(t, err)

		logger.Info("ignored info message")
		assert.Empty(t, buffer.String())

		logger.Warn("visible warning", "code", 42)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(buffer.Bytes(), &decoded))
		assert.Equal(t, "WARN", decoded["level"])
		assert.Equal(t, "visible warning", decoded["msg"])
		assert.Equal(t, float64(42), decoded["code"])
	})

	t.Run("environment variables provide defaults", func(t *testing.T) {
		t.Setenv(EnvLogLevel, "info")
		t.Setenv(EnvLogFormat, "json")

		var buffer bytes.Buffer
		logger, err := New(Options{Writer: &buffer})
		require.NoError(t, err)

		logger.Info("from env")
		assert.Contains(t, buffer.String(), `"msg":"from env"`)
	})

	t.Run("invalid level and format", func(t *testing.T) {
		_, err := New(Options{Level: "verbose"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid log level")

		_, err = New(Options{Level: "info", Format: "xml"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid log format")
	})
}

func TestParseLevel(t *testing.T) {
	level, err := ParseLevel("WARNING")
	require.NoError(t, err)
	assert.Equal(t, slog.LevelWarn, level)

	level, err = ParseLevel("error")
	require.NoError(t, err)
	assert.Equal(t, slog.LevelError, level)
}
