// Package security enforces permission profiles, destructive command
// confirmations, audit logging, and sliding-window rate limiting.
package security

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cafecito-games/vox/internal/config"
)

const (
	// EnvProfile overrides the persisted active permission profile for a single invocation.
	EnvProfile = "VOX_PROFILE"
)

// ErrPermissionDenied indicates a command was blocked by the active permission profile.
var ErrPermissionDenied = errors.New("permission denied")

// ErrAborted indicates the user declined a destructive confirmation prompt.
var ErrAborted = errors.New("aborted")

// Profile defines the command allowlist and denylist for an access control profile.
type Profile struct {
	Description string   `json:"description"`
	Allowed     []string `json:"allowed"`
	Denied      []string `json:"denied"`
}

// PermissionsFile is the JSON schema stored in ~/.vox/permissions.json.
type PermissionsFile struct {
	ActiveProfile string             `json:"active_profile"`
	Profiles      map[string]Profile `json:"profiles"`
}

// BuiltinProfileNames lists the canonical built-in profile identifiers in display order.
var BuiltinProfileNames = []string{"full", "chat", "readonly", "moderation"}

// DefaultProfiles returns a fresh copy of the four built-in permission profiles.
//
// Pattern rules match exact command paths or space-delimited prefixes (for
// example, "message" matches "message send" but not "messaging"). Bare group
// names are only granted where blanket access is intentionally desired.
func DefaultProfiles() map[string]Profile {
	return map[string]Profile{
		"full": {
			Description: "Full access to all commands",
			Allowed:     []string{"*"},
			Denied:      []string{},
		},
		"chat": {
			Description: "Messages, reactions, threads, typing, interactions only",
			Allowed: []string{
				"message",
				"reaction",
				"thread",
				"typing",
				"dm",
				"listen",
				"serve",
				"interact",
				"config show",
				"server list",
				"server info",
				"schema",
			},
			Denied: []string{
				"member kick",
				"member ban",
				"member unban",
				"member timeout",
				"channel delete",
				"role delete",
				"role create",
				"channel create",
				"message bulk-delete",
				"webhook delete",
				"event delete",
				"voice",
			},
		},
		"readonly": {
			Description: "Read-only: list, info, get, search, listen",
			Allowed: []string{
				"message list",
				"message get",
				"message search",
				"message search-server",
				"message history",
				"message pins",
				"channel list",
				"channel info",
				"server list",
				"server info",
				"server audit-log",
				"server export",
				"server diff",
				"server onboarding show",
				"role list",
				"member list",
				"member info",
				"reaction list",
				"reaction users",
				"thread list",
				"poll results",
				"webhook list",
				"event list",
				"invite list",
				"invite info",
				"emoji list",
				"automod list",
				"schedule list",
				"listen",
				"config show",
				"permission show",
				"permission profiles",
				"audit show",
				"doctor",
				"bot show",
				"bot invite",
				"voice status",
				"voice where",
				"voice members",
				"schema",
			},
			Denied: []string{"*"},
		},
		"moderation": {
			Description: "Moderation, voice, and interactions. No structural or security changes.",
			Allowed: []string{
				"audit show",
				"config show",
				"doctor",
				"bot show",
				"bot invite",
				"listen",
				"serve",
				"schema",
				"server list",
				"server info",
				"server audit-log",
				"server export",
				"server diff",
				"server onboarding show",
				"channel list",
				"channel info",
				"role list",
				"member list",
				"member info",
				"emoji list",
				"event list",
				"webhook list",
				"poll results",
				"permission show",
				"permission profiles",
				"schedule list",
				"invite list",
				"invite info",
				"message list",
				"message get",
				"message history",
				"message search",
				"message search-server",
				"message pins",
				"reaction list",
				"reaction users",
				"member kick",
				"member ban",
				"member unban",
				"member timeout",
				"member nick",
				"role assign",
				"role remove",
				"message delete",
				"message bulk-delete",
				"message pin",
				"message unpin",
				"reaction remove",
				"reaction add",
				"invite delete",
				"channel edit",
				"channel set-permissions",
				"thread",
				"automod",
				"voice",
				"interact",
				"message send",
				"message reply",
				"message edit",
				"dm send",
				"dm list",
				"typing",
			},
			Denied: []string{},
		},
	}
}

// DestructiveCommands lists every command path requiring explicit confirmation
// (unless bypassed with --yes/-y).
var DestructiveCommands = map[string]struct{}{
	"member kick":          {},
	"member ban":           {},
	"member unban":         {},
	"member timeout":       {},
	"channel delete":       {},
	"message delete":       {},
	"message bulk-delete":  {},
	"role delete":          {},
	"webhook delete":       {},
	"event delete":         {},
	"emoji delete":         {},
	"invite delete":        {},
	"schedule remove":      {},
	"automod delete":       {},
	"server apply --prune": {},
}

// IsDestructive reports whether commandPath requires confirmation before execution.
func IsDestructive(commandPath string) bool {
	_, ok := DestructiveCommands[NormalizeCommandPath(commandPath)]
	return ok
}

// NormalizeCommandPath strips a leading binary prefix ("vox " or "main ") and
// trims surrounding whitespace.
func NormalizeCommandPath(commandPath string) string {
	trimmed := strings.TrimSpace(commandPath)
	for _, prefix := range []string{"vox ", "main "} {
		if after, ok := strings.CutPrefix(trimmed, prefix); ok {
			trimmed = strings.TrimSpace(after)
		}
	}
	return trimmed
}

// MatchesPattern reports whether pattern matches commandPath using exact match,
// wildcard "*", or space-delimited prefix semantics.
func MatchesPattern(pattern, commandPath string) bool {
	pattern = strings.TrimSpace(pattern)
	commandPath = NormalizeCommandPath(commandPath)
	if pattern == "*" {
		return true
	}
	if commandPath == pattern {
		return true
	}
	return strings.HasPrefix(commandPath, pattern+" ")
}

// EvaluateProfile determines whether commandPath is allowed under profile.
//
// Evaluation order:
//  1. Denied list is evaluated first.
//  2. If "*" appears in Denied, everything is denied unless explicitly matched by Allowed.
//  3. Otherwise, any specific Denied match blocks the command.
//  4. If no Denied rule matched, at least one Allowed pattern must match.
func EvaluateProfile(profile Profile, commandPath string) bool {
	normalized := NormalizeCommandPath(commandPath)
	allowed := profile.Allowed
	if allowed == nil {
		allowed = []string{"*"}
	}

	for _, pattern := range profile.Denied {
		if pattern == "*" {
			for _, allowPattern := range allowed {
				if MatchesPattern(allowPattern, normalized) {
					return true
				}
			}
			return false
		}
		if MatchesPattern(pattern, normalized) {
			return false
		}
	}

	for _, allowPattern := range allowed {
		if MatchesPattern(allowPattern, normalized) {
			return true
		}
	}
	return false
}

// Manager coordinates profile persistence, command authorization, confirmation
// prompts, and audit logging rooted in a config.Store.
type Manager struct {
	Store   *config.Store
	Audit   *AuditLogger
	Limiter *RateLimiter
}

// NewManager constructs a security manager backed by store.
func NewManager(store *config.Store) *Manager {
	return &Manager{
		Store:   store,
		Audit:   NewAuditLogger(store.AuditLogPath()),
		Limiter: NewRateLimiter(5, 5*time.Second),
	}
}

// LoadPermissionsFile loads permissions.json, returning defaults if absent or invalid.
func (manager *Manager) LoadPermissionsFile() PermissionsFile {
	raw, err := os.ReadFile(manager.Store.PermissionsPath())
	if err != nil {
		return PermissionsFile{ActiveProfile: "full", Profiles: map[string]Profile{}}
	}
	var parsed PermissionsFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return PermissionsFile{ActiveProfile: "full", Profiles: map[string]Profile{}}
	}
	if parsed.ActiveProfile == "" {
		parsed.ActiveProfile = "full"
	}
	if parsed.Profiles == nil {
		parsed.Profiles = map[string]Profile{}
	}
	return parsed
}

// Profiles returns built-in profiles merged with any custom profiles defined in permissions.json.
func (manager *Manager) Profiles() map[string]Profile {
	merged := DefaultProfiles()
	file := manager.LoadPermissionsFile()
	maps.Copy(merged, file.Profiles)
	return merged
}

// ActiveProfileName returns the persisted active profile name (default "full").
func (manager *Manager) ActiveProfileName() string {
	return manager.LoadPermissionsFile().ActiveProfile
}

// ActiveProfile resolves the effective profile name and definition, applying
// explicit override (--profile), then VOX_PROFILE, then permissions.json.
func (manager *Manager) ActiveProfile(override string) (string, Profile, error) {
	chosen := strings.TrimSpace(override)
	if chosen == "" {
		chosen = strings.TrimSpace(os.Getenv(EnvProfile))
	}

	profiles := manager.Profiles()
	if chosen != "" {
		profile, ok := profiles[chosen]
		if !ok {
			valid := slices.Sorted(maps.Keys(profiles))
			return "", Profile{}, fmt.Errorf("unknown permission profile %q (available: %s)", chosen, strings.Join(valid, ", "))
		}
		return chosen, profile, nil
	}

	activeName := manager.ActiveProfileName()
	profile, ok := profiles[activeName]
	if !ok {
		return "full", DefaultProfiles()["full"], nil
	}
	return activeName, profile, nil
}

// SetActiveProfile persists active_profile in permissions.json.
func (manager *Manager) SetActiveProfile(name string) (Profile, error) {
	profiles := manager.Profiles()
	profile, ok := profiles[name]
	if !ok {
		valid := slices.Sorted(maps.Keys(profiles))
		return Profile{}, fmt.Errorf("unknown permission profile %q (available: %s)", name, strings.Join(valid, ", "))
	}

	if err := config.EnsureSecureDir(manager.Store.BaseDir); err != nil {
		return Profile{}, err
	}

	file := manager.LoadPermissionsFile()
	file.ActiveProfile = name
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return Profile{}, fmt.Errorf("encode permissions: %w", err)
	}
	payload = append(payload, '\n')
	if err := config.WriteSecureFile(manager.Store.PermissionsPath(), payload); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Enforce checks whether commandPath is permitted under the active profile (or override).
func (manager *Manager) Enforce(commandPath, profileOverride string) error {
	normalized := NormalizeCommandPath(commandPath)
	name, profile, err := manager.ActiveProfile(profileOverride)
	if err != nil {
		return err
	}
	if !EvaluateProfile(profile, normalized) {
		return fmt.Errorf(
			"%w: command %q is denied by the %q permission profile (run `vox permission show` to inspect)",
			ErrPermissionDenied,
			normalized,
			name,
		)
	}
	return nil
}

// ConfirmOptions configures interactive destructive command confirmation.
type ConfirmOptions struct {
	Context         context.Context
	CommandPath     string
	Details         string
	ProfileOverride string
	SkipConfirm     bool
	Interactive     bool
	In              io.Reader
	Out             io.Writer
}

// ConfirmDestructive enforces the active permission profile first (failing closed
// before prompting), then prompts the user if the command is destructive and
// --yes was not supplied.
func (manager *Manager) ConfirmDestructive(options ConfirmOptions) error {
	normalized := NormalizeCommandPath(options.CommandPath)
	if !IsDestructive(normalized) {
		return nil
	}
	if err := manager.Enforce(normalized, options.ProfileOverride); err != nil {
		return err
	}
	if options.SkipConfirm {
		return nil
	}
	if !options.Interactive {
		return fmt.Errorf(
			"%w: destructive action %q requires confirmation in an interactive terminal or --yes/-y",
			ErrAborted,
			normalized,
		)
	}

	prompt := fmt.Sprintf("⚠ Destructive action: %s", normalized)
	if strings.TrimSpace(options.Details) != "" {
		prompt += fmt.Sprintf(" (%s)", options.Details)
	}
	prompt += ". Continue? [y/N]: "

	out := options.Out
	if out == nil {
		out = os.Stderr
	}
	in := options.In
	if in == nil {
		in = os.Stdin
	}

	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrAborted, context.Canceled)
	}

	if _, err := fmt.Fprint(out, prompt); err != nil {
		return err
	}
	type lineResult struct {
		text string
		err  error
	}
	reader := bufio.NewReader(in)
	resultCh := make(chan lineResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		resultCh <- lineResult{text: line, err: err}
	}()

	var line string
	select {
	case <-ctx.Done():
		_, _ = fmt.Fprintln(out)
		return fmt.Errorf("%w: %w", ErrAborted, context.Canceled)
	case res := <-resultCh:
		if res.err != nil && !errors.Is(res.err, io.EOF) {
			return fmt.Errorf("read confirmation: %w", res.err)
		}
		line = res.text
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("%w: cancelled by user", ErrAborted)
	}
	return nil
}

// AuditEntry represents one NDJSON line in ~/.vox/audit.log.
type AuditEntry struct {
	Timestamp string         `json:"timestamp"`
	Command   string         `json:"command"`
	Args      map[string]any `json:"args"`
	Result    string         `json:"result"`
	User      string         `json:"user"`
}

// AuditLogger appends and queries structured audit records.
type AuditLogger struct {
	path  string
	mutex sync.Mutex
	now   func() time.Time
}

// NewAuditLogger creates an AuditLogger writing to path.
func NewAuditLogger(path string) *AuditLogger {
	return &AuditLogger{
		path: path,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// Record appends an audit entry to the log file.
func (logger *AuditLogger) Record(command string, args map[string]any, result, user string) error {
	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	if err := config.EnsureSecureDir(filepath.Dir(logger.path)); err != nil {
		return err
	}
	if args == nil {
		args = map[string]any{}
	}
	if result == "" {
		result = "ok"
	}

	entry := AuditEntry{
		Timestamp: logger.now().UTC().Format(time.RFC3339Nano),
		Command:   command,
		Args:      args,
		Result:    result,
		User:      user,
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode audit entry: %w", err)
	}
	encoded = append(encoded, '\n')

	file, err := os.OpenFile(logger.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// Read returns up to the last limit audit entries. If limit <= 0, all entries are returned.
func (logger *AuditLogger) Read(limit int) ([]AuditEntry, error) {
	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	raw, err := os.ReadFile(logger.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuditEntry{}, nil
		}
		return nil, fmt.Errorf("read audit log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	entries := make([]AuditEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse audit log entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// Clear removes the audit log file if it exists.
func (logger *AuditLogger) Clear() error {
	logger.mutex.Lock()
	defer logger.mutex.Unlock()

	err := os.Remove(logger.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear audit log: %w", err)
	}
	return nil
}

// RateLimiter implements a thread-safe sliding-window limiter for moderation actions.
type RateLimiter struct {
	maxCalls int
	period   time.Duration
	mutex    sync.Mutex
	calls    []time.Time
	now      func() time.Time
	sleep    func(time.Duration)
}

// NewRateLimiter constructs a RateLimiter permitting at most maxCalls within period.
func NewRateLimiter(maxCalls int, period time.Duration) *RateLimiter {
	return &RateLimiter{
		maxCalls: maxCalls,
		period:   period,
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

// Wait blocks if maxCalls have already occurred within period, invoking onWait
// (if non-nil) before sleeping so callers can surface a notice on stderr.
func (limiter *RateLimiter) Wait(onWait func(time.Duration)) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	current := limiter.now()
	cutoff := current.Add(-limiter.period)

	kept := limiter.calls[:0]
	for _, callTime := range limiter.calls {
		if callTime.After(cutoff) {
			kept = append(kept, callTime)
		}
	}
	limiter.calls = kept

	if len(limiter.calls) >= limiter.maxCalls {
		waitDuration := limiter.period - current.Sub(limiter.calls[0])
		if waitDuration > 0 {
			if onWait != nil {
				onWait(waitDuration)
			}
			limiter.sleep(waitDuration)
		}
	}
	limiter.calls = append(limiter.calls, limiter.now())
}
