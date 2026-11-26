package scheduler

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIntervalAndWallClock(t *testing.T) {
	seconds, err := ParseInterval("30s")
	require.NoError(t, err)
	assert.Equal(t, 30, seconds)

	seconds, err = ParseInterval("2h")
	require.NoError(t, err)
	assert.Equal(t, 7200, seconds)

	_, err = ParseInterval("10s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 30 seconds")

	_, err = ParseInterval("5w")
	require.Error(t, err)

	hour, minute, location, tz, err := ParseWallClock("09:30", "America/New_York")
	require.NoError(t, err)
	assert.Equal(t, 9, hour)
	assert.Equal(t, 30, minute)
	assert.Equal(t, "America/New_York", tz)
	assert.NotNil(t, location)

	_, _, _, _, err = ParseWallClock("25:00", "UTC")
	require.Error(t, err)

	_, _, _, _, err = ParseWallClock("09:00", "Not/A_Zone")
	require.Error(t, err)
}

func TestParseActionSafety(t *testing.T) {
	// Allows Discord markdown backticks and dollar signs inside quotes.
	argv, err := ParseAction(`vox message send "#general" "Deploy \`+"`v1.2.3`"+` costs $0"`, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"message", "send", "#general", `Deploy \` + "`v1.2.3`" + ` costs $0`}, argv)

	// Rejects shell chaining and subshells.
	for _, unsafe := range []string{
		`message send general hi && rm -rf /`,
		`message send general hi; echo pwned`,
		`message send general $(whoami)`,
		`message send general hi | cat`,
	} {
		_, err := ParseAction(unsafe, nil)
		require.Error(t, err, "expected error for %q", unsafe)
	}

	// Rejects interactive and non-terminating commands.
	for _, blocked := range []string{"setup", "serve", "listen", "schedule list"} {
		_, err := ParseAction(blocked, nil)
		require.Error(t, err, "expected error for %q", blocked)
	}
}

func TestStoreRoundTripAndRunner(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	count := 1
	entry := Entry{
		Name:       "heartbeat",
		Action:     `message send general "ping"`,
		Argv:       []string{"message", "send", "general", "ping"},
		Enabled:    true,
		Count:      &count,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Every:      0, // 0 triggers immediately in unit test
		EveryHuman: "1s",
	}
	require.NoError(t, store.Save([]Entry{entry}))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Contains(t, loaded[0].Describe(), "heartbeat — every 1s")

	var executed atomic.Int32
	runner := &Runner{
		Store: store,
		Execute: func(_ context.Context, argv []string) error {
			assert.Equal(t, []string{"message", "send", "general", "ping"}, argv)
			executed.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, runner.Run(ctx, []string{"heartbeat"}))
	assert.Equal(t, int32(1), executed.Load())

	updated, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, updated[0].LastResult)
	assert.Equal(t, "ok", *updated[0].LastResult)
}
