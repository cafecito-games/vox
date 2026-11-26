package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriterPrintPlainAndJSON(t *testing.T) {
	payload := map[string]any{"id": "123", "ok": true}

	t.Run("plain mode", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		writer := &Writer{Stdout: &stdout, Stderr: &stderr, JSON: false, NoColor: true}

		require.NoError(t, writer.Print(payload, "Created 123"))
		assert.Equal(t, "Created 123\n", stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("json mode", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		writer := &Writer{Stdout: &stdout, Stderr: &stderr, JSON: true, NoColor: true}

		require.NoError(t, writer.Print(payload, "Created 123"))
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &decoded))
		assert.Equal(t, "123", decoded["id"])
		assert.Equal(t, true, decoded["ok"])
	})

	t.Run("stream jsonl mode stays single-line", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		writer := &Writer{Stdout: &stdout, Stderr: &stderr, JSON: true, NoColor: true}

		require.NoError(t, writer.PrintStream(payload, "plain"))
		assert.Equal(t, "{\"id\":\"123\",\"ok\":true}\n", stdout.String())
	})
}

func TestWriterStderrAndErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	writer := &Writer{Stdout: &stdout, Stderr: &stderr, JSON: true, NoColor: true}

	writer.WarnChannelVisibility()
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "2026-11-16")

	stderr.Reset()
	err := &CLIError{
		Code:     "not_found",
		Message:  "Channel not found: general",
		Hint:     "Run `vox channel list` to inspect visible channels.",
		ExitCode: ExitError,
	}
	writer.PrintError(err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &decoded))
	assert.Equal(t, "not_found", decoded["code"])
	assert.Equal(t, "Channel not found: general", decoded["error"])
	assert.Contains(t, decoded["hint"], "vox channel list")
	assert.Equal(t, ExitError, ExitCodeFor(err))
	assert.Equal(t, ExitPermissionDenied, ExitCodeFor(PermissionError("denied", errors.New("boom"))))
	assert.Equal(t, ExitOK, ExitCodeFor(nil))
}
