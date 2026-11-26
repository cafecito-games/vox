package security

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cafecito-games/vox/internal/config"
)

func TestPermissionProfilesInvariants(t *testing.T) {
	profiles := DefaultProfiles()

	// Moderation must be an explicit allowlist, not wildcard "*", and must not
	// allow self-escalation via `permission set`.
	moderation := profiles["moderation"]
	assert.NotContains(t, moderation.Allowed, "*")
	assert.False(t, EvaluateProfile(moderation, "permission set"))
	assert.False(t, EvaluateProfile(moderation, "config set"))
	assert.True(t, EvaluateProfile(moderation, "voice join"))
	assert.True(t, EvaluateProfile(moderation, "interact modal"))
	assert.True(t, EvaluateProfile(moderation, "member kick"))

	// Moderation must be a superset of readonly.
	readonly := profiles["readonly"]
	for _, allowed := range readonly.Allowed {
		assert.True(t, EvaluateProfile(moderation, allowed), "moderation must allow readonly command %q", allowed)
	}

	// Chat must allow messaging/interactions and block destructive/structural ops and voice.
	chat := profiles["chat"]
	assert.True(t, EvaluateProfile(chat, "message send"))
	assert.True(t, EvaluateProfile(chat, "config show"))
	assert.False(t, EvaluateProfile(chat, "config set"))
	assert.False(t, EvaluateProfile(chat, "server apply"))
	assert.False(t, EvaluateProfile(chat, "channel delete"))
	assert.False(t, EvaluateProfile(chat, "voice join"))
	assert.False(t, EvaluateProfile(chat, "member kick"))

	// Readonly must block all mutating commands.
	assert.True(t, EvaluateProfile(readonly, "message list"))
	assert.True(t, EvaluateProfile(readonly, "server diff"))
	assert.False(t, EvaluateProfile(readonly, "message send"))
	assert.False(t, EvaluateProfile(readonly, "server apply"))
	assert.False(t, EvaluateProfile(readonly, "serve"))

	// Prefix matching must be space-delimited ("message" must not match "messaging").
	assert.True(t, MatchesPattern("message", "message send"))
	assert.False(t, MatchesPattern("message", "messaging send"))
}

func TestManagerPersistenceAndOverride(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	manager := NewManager(store)

	t.Setenv(EnvProfile, "")

	assert.Equal(t, "full", manager.ActiveProfileName())
	require.NoError(t, manager.Enforce("channel delete", ""))

	_, err = manager.SetActiveProfile("readonly")
	require.NoError(t, err)
	assert.Equal(t, "readonly", manager.ActiveProfileName())

	err = manager.Enforce("channel delete", "")
	require.ErrorIs(t, err, ErrPermissionDenied)

	// Per-invocation override takes precedence over persisted profile.
	require.NoError(t, manager.Enforce("channel delete", "full"))

	// Environment override works when flag override is empty.
	t.Setenv(EnvProfile, "full")
	require.NoError(t, manager.Enforce("channel delete", ""))
}

func TestConfirmDestructiveFailsClosedBeforePrompting(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	manager := NewManager(store)
	_, err = manager.SetActiveProfile("readonly")
	require.NoError(t, err)

	var promptOut bytes.Buffer
	err = manager.ConfirmDestructive(ConfirmOptions{
		CommandPath: "channel delete",
		Details:     "#general",
		Interactive: true,
		In:          strings.NewReader("y\n"),
		Out:         &promptOut,
	})
	require.ErrorIs(t, err, ErrPermissionDenied)
	assert.Empty(t, promptOut.String(), "denied profile must fail before prompting")
}

func TestConfirmDestructiveInteractiveAndYesFlag(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	manager := NewManager(store)

	// --yes bypasses prompt even when non-interactive.
	require.NoError(t, manager.ConfirmDestructive(ConfirmOptions{
		CommandPath: "message delete",
		SkipConfirm: true,
		Interactive: false,
	}))

	// Non-interactive without --yes aborts cleanly.
	err = manager.ConfirmDestructive(ConfirmOptions{
		CommandPath: "message delete",
		Interactive: false,
	})
	require.ErrorIs(t, err, ErrAborted)

	// Interactive "y" confirms.
	var out bytes.Buffer
	require.NoError(t, manager.ConfirmDestructive(ConfirmOptions{
		CommandPath: "message delete",
		Details:     "msg 123",
		Interactive: true,
		In:          strings.NewReader("y\n"),
		Out:         &out,
	}))
	assert.Contains(t, out.String(), "Destructive action: message delete (msg 123)")

	// Interactive "n" aborts.
	err = manager.ConfirmDestructive(ConfirmOptions{
		CommandPath: "message delete",
		Interactive: true,
		In:          strings.NewReader("n\n"),
		Out:         &bytes.Buffer{},
	})
	require.ErrorIs(t, err, ErrAborted)
}

func TestAuditLoggerRoundTrip(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	require.NoError(t, err)
	audit := NewAuditLogger(store.AuditLogPath())

	entries, err := audit.Read(10)
	require.NoError(t, err)
	assert.Empty(t, entries)

	require.NoError(t, audit.Record("member kick", map[string]any{"member": "alice"}, "ok", "999"))
	require.NoError(t, audit.Record("role delete", map[string]any{"role": "temp"}, "ok", ""))
	require.NoError(t, audit.Record("channel delete", map[string]any{"channel": "old"}, "ok", ""))

	limited, err := audit.Read(2)
	require.NoError(t, err)
	require.Len(t, limited, 2)
	assert.Equal(t, "role delete", limited[0].Command)
	assert.Equal(t, "channel delete", limited[1].Command)

	require.NoError(t, audit.Clear())
	afterClear, err := audit.Read(10)
	require.NoError(t, err)
	assert.Empty(t, afterClear)
}

func TestRateLimiterEnforcesSlidingWindow(t *testing.T) {
	current := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var slept time.Duration
	var notified time.Duration

	limiter := NewRateLimiter(2, 5*time.Second)
	limiter.now = func() time.Time { return current }
	limiter.sleep = func(duration time.Duration) {
		slept += duration
		current = current.Add(duration)
	}

	limiter.Wait(nil)
	current = current.Add(1 * time.Second)
	limiter.Wait(nil)
	assert.Zero(t, slept)

	current = current.Add(1 * time.Second)
	limiter.Wait(func(d time.Duration) { notified = d })
	assert.Equal(t, 3*time.Second, slept)
	assert.Equal(t, 3*time.Second, notified)
}
