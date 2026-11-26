package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/output"
)

func TestSplitMessageChunksPreservesUTF8(t *testing.T) {
	assert.Equal(t, []string{"(empty response)"}, SplitMessageChunks("", 2000))
	assert.Equal(t, []string{"hello"}, SplitMessageChunks("hello", 2000))

	long := strings.Repeat("🔥", 2005)
	chunks := SplitMessageChunks(long, 2000)
	require.Len(t, chunks, 2)
	assert.Equal(t, 2000, len([]rune(chunks[0])))
	assert.Equal(t, 5, len([]rune(chunks[1])))
}

func TestAllActionsCatalogIsCompleteAndUnique(t *testing.T) {
	assert.Len(t, AllActions, 73)
	seen := make(map[string]struct{}, len(AllActions))
	for _, action := range AllActions {
		assert.NotEmpty(t, action.Name)
		assert.NotEmpty(t, action.Category)
		_, duplicate := seen[action.Name]
		assert.False(t, duplicate, "duplicate serve action %q", action.Name)
		seen[action.Name] = struct{}{}
	}
}

func TestReadStdinHandlesInvalidJSONAndUnknownActions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	out := &output.Writer{Stdout: &stdout, Stderr: &stderr, JSON: true, NoColor: true}

	stdin := strings.Join([]string{
		`{invalid json`,
		`{"req_id":"1"}`,
		`{"action":"does_not_exist","req_id":"2"}`,
	}, "\n") + "\n"

	daemon := NewDaemon(Options{
		In:  strings.NewReader(stdin),
		Out: out,
	})
	daemon.readStdin(context.Background())

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 3)

	var line0, line1, line2 map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &line0))
	assert.Equal(t, "error", line0["event"])
	assert.Contains(t, line0["message"], "Invalid JSON")

	require.NoError(t, json.Unmarshal([]byte(lines[1]), &line1))
	assert.Equal(t, "response", line1["event"])
	assert.Equal(t, "1", line1["req_id"])
	assert.Contains(t, line1["error"], "Missing 'action'")

	require.NoError(t, json.Unmarshal([]byte(lines[2]), &line2))
	assert.Equal(t, "response", line2["event"])
	assert.Equal(t, "2", line2["req_id"])
	assert.Contains(t, line2["error"], "unknown action")
}
