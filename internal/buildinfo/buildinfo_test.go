package buildinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurrent(t *testing.T) {
	info := Current()
	require.NotEmpty(t, info.Version)
	require.NotEmpty(t, info.GoVersion)
	assert.Contains(t, info.String(), "vox version "+info.Version)
}

func TestInfoStringFormatting(t *testing.T) {
	tests := []struct {
		name     string
		info     Info
		expected string
	}{
		{
			name:     "release with commit and date",
			info:     Info{Version: "1.2.3", Commit: "0123456789abcdef", Date: "2026-11-26T12:00:00Z"},
			expected: "vox version 1.2.3 (commit 0123456789ab), built 2026-11-26T12:00:00Z",
		},
		{
			name:     "dirty with commit",
			info:     Info{Version: "dev", Commit: "abcdef1234", Dirty: true},
			expected: "vox version dev (commit abcdef1234, dirty)",
		},
		{
			name:     "dirty without commit",
			info:     Info{Version: "dev", Dirty: true},
			expected: "vox version dev (dirty)",
		},
		{
			name:     "minimal dev",
			info:     Info{Version: "dev"},
			expected: "vox version dev",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, testCase.info.String())
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	assert.Equal(t, "dev", normalizeVersion(""))
	assert.Equal(t, "dev", normalizeVersion("(devel)"))
	assert.Equal(t, "0.4.2", normalizeVersion("v0.4.2"))
	assert.Equal(t, "0.4.2", normalizeVersion("  0.4.2  "))
}
