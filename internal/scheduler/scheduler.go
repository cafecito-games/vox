// Package scheduler persists and executes recurring vox CLI commands on
// wall-clock schedules (with IANA timezone support) or fixed intervals.
//
// Scheduled actions are lexed and dispatched directly against vox's command
// tree without ever invoking a system shell.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Embed IANA timezone database so Windows has full timezone support.

	"github.com/cafecito-games/vox/internal/config"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
)

// MinIntervalSeconds enforces a minimum 30-second floor for interval schedules.
const MinIntervalSeconds = 30

var (
	durationPattern = regexp.MustCompile(`^(\d+)([smhd])$`)
	shellOperators  = map[string]struct{}{
		"&&": {}, "||": {}, "|": {}, ";": {}, "&": {},
		">": {}, ">>": {}, "<": {}, "<<": {}, "(": {}, ")": {},
	}
	interactiveCommands = map[string]struct{}{
		"setup": {},
	}
	nonTerminatingCommands = map[string]struct{}{
		"serve":    {},
		"listen":   {},
		"schedule": {},
	}
)

// Entry describes one persisted schedule in ~/.vox/schedules.json.
type Entry struct {
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	Argv       []string `json:"argv"`
	Enabled    bool     `json:"enabled"`
	Count      *int     `json:"count"`
	CreatedAt  string   `json:"created_at"`
	LastRun    *string  `json:"last_run"`
	LastResult *string  `json:"last_result"`

	// Wall-clock schedule fields (mutually exclusive with Every/EveryHuman).
	Time string `json:"time,omitempty"`
	TZ   string `json:"tz,omitempty"`

	// Interval schedule fields.
	Every      int    `json:"every,omitempty"`
	EveryHuman string `json:"every_human,omitempty"`
}

// Describe returns a concise single-line human summary of a schedule entry.
func (entry Entry) Describe() string {
	disabled := ""
	if !entry.Enabled {
		disabled = " [disabled]"
	}
	cadence := ""
	if entry.Time != "" {
		tz := entry.TZ
		if tz == "" {
			tz = "UTC"
		}
		cadence = fmt.Sprintf("daily at %s %s", entry.Time, tz)
	} else {
		human := entry.EveryHuman
		if human == "" {
			human = fmt.Sprintf("%ds", entry.Every)
		}
		cadence = "every " + human
	}
	summary := fmt.Sprintf("%s%s — %s — %s", entry.Name, disabled, cadence, entry.Action)
	if entry.LastRun != nil && *entry.LastRun != "" {
		ts := *entry.LastRun
		if len(ts) >= 19 {
			ts = strings.Replace(ts[:19], "T", " ", 1)
		}
		res := ""
		if entry.LastResult != nil {
			res = *entry.LastResult
		}
		summary += fmt.Sprintf(" (last run %s: %s)", ts, res)
	}
	return summary
}

type schedulesFile struct {
	Schedules []Entry `json:"schedules"`
}

// Store reads and writes schedules.json safely under a mutex.
type Store struct {
	path  string
	mutex sync.Mutex
}

// NewStore creates a schedule store targeting path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load returns all configured schedules from disk.
func (store *Store) Load() ([]Entry, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.loadLocked()
}

func (store *Store) loadLocked() ([]Entry, error) {
	raw, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("read schedules %s: %w", store.path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []Entry{}, nil
	}

	// Support both {"schedules": [...]} and bare [...] for backward compatibility.
	if strings.HasPrefix(trimmed, "[") {
		var bare []Entry
		if err := json.Unmarshal([]byte(trimmed), &bare); err != nil {
			return nil, fmt.Errorf("parse schedules: %w", err)
		}
		return bare, nil
	}

	var wrapper schedulesFile
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
		return nil, fmt.Errorf("parse schedules: %w", err)
	}
	if wrapper.Schedules == nil {
		return []Entry{}, nil
	}
	return wrapper.Schedules, nil
}

// Save writes schedules to disk atomically with secure file permissions.
func (store *Store) Save(entries []Entry) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.saveLocked(entries)
}

func (store *Store) saveLocked(entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	payload, err := json.MarshalIndent(schedulesFile{Schedules: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedules: %w", err)
	}
	payload = append(payload, '\n')
	if err := config.EnsureSecureDir(dirOf(store.path)); err != nil {
		return err
	}
	return config.WriteSecureFile(store.path, payload)
}

// UpdateLastRun updates last_run and last_result for the named schedule on disk.
func (store *Store) UpdateLastRun(name, result string, runTime time.Time) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	entries, err := store.loadLocked()
	if err != nil {
		return err
	}
	timestamp := runTime.UTC().Format(time.RFC3339)
	for i := range entries {
		if entries[i].Name == name {
			entries[i].LastRun = &timestamp
			entries[i].LastResult = &result
			return store.saveLocked(entries)
		}
	}
	return nil
}

func dirOf(path string) string {
	idx := strings.LastIndexAny(path, `/\`)
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}

// ParseInterval parses an interval string such as "30s", "5m", "2h", or "1d"
// into seconds, enforcing MinIntervalSeconds.
func ParseInterval(raw string) (int, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	match := durationPattern.FindStringSubmatch(normalized)
	if match == nil {
		return 0, output.UsageError(fmt.Sprintf("invalid interval %q (use e.g. 30s, 5m, 2h, 1d)", raw))
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, output.UsageError(fmt.Sprintf("invalid interval %q", raw))
	}
	var multiplier int
	switch match[2] {
	case "s":
		multiplier = 1
	case "m":
		multiplier = 60
	case "h":
		multiplier = 3600
	case "d":
		multiplier = 86400
	}
	seconds := amount * multiplier
	if seconds < MinIntervalSeconds {
		return 0, output.UsageError(fmt.Sprintf(
			"interval must be at least %d seconds (got %q)",
			MinIntervalSeconds,
			raw,
		))
	}
	return seconds, nil
}

// ParseWallClock validates a 24-hour "HH:MM" time and optional IANA timezone name.
func ParseWallClock(rawTime, tzName string) (int, int, *time.Location, string, error) {
	parts := strings.Split(strings.TrimSpace(rawTime), ":")
	if len(parts) != 2 {
		return 0, 0, nil, "", output.UsageError(fmt.Sprintf("invalid time %q (expected 24-hour HH:MM)", rawTime))
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMin := strconv.Atoi(parts[1])
	if errHour != nil || errMin != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, nil, "", output.UsageError(fmt.Sprintf("invalid time %q (expected 00:00..23:59)", rawTime))
	}

	tz := strings.TrimSpace(tzName)
	if tz == "" {
		tz = "UTC"
	}
	location, err := time.LoadLocation(tz)
	if err != nil {
		return 0, 0, nil, "", output.UsageError(fmt.Sprintf("unknown timezone %q: %v", tz, err))
	}
	return hour, minute, location, tz, nil
}

// CommandValidator checks whether a lexed argv resolves to a real vox command.
type CommandValidator func(argv []string) error

// ParseAction lexes a scheduled action string without invoking a shell, rejects
// shell control operators and interactive/non-terminating commands, and validates
// the command path via validate if provided.
//
// Backticks and "$" are intentionally permitted so Discord markdown code blocks
// and dollar signs work naturally in scheduled messages.
func ParseAction(action string, validate CommandValidator) ([]string, error) {
	tokens, err := lexAction(action)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, output.UsageError("action cannot be empty")
	}

	if tokens[0] == "vox" {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return nil, output.UsageError("action must include a command after binary name")
	}

	for _, token := range tokens {
		if _, forbidden := shellOperators[token]; forbidden {
			return nil, output.UsageError(fmt.Sprintf(
				"shell operator %q is not allowed in scheduled actions; schedule actions are vox command lines, not shell scripts",
				token,
			))
		}
	}

	rootCmd := tokens[0]
	if _, ok := interactiveCommands[rootCmd]; ok {
		return nil, output.UsageError(fmt.Sprintf("command %q requires an interactive human and cannot be scheduled", rootCmd))
	}
	if _, ok := nonTerminatingCommands[rootCmd]; ok {
		return nil, output.UsageError(fmt.Sprintf("command %q does not terminate and cannot be scheduled", rootCmd))
	}

	if validate != nil {
		if err := validate(tokens); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

// lexAction splits a command line into POSIX-like argv tokens while isolating
// unquoted shell operator tokens (&&, ||, |, ;, &, >, >>, <, <<, (, )).
func lexAction(input string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inToken := false
	var quote rune

	flush := func() {
		if inToken {
			tokens = append(tokens, current.String())
			current.Reset()
			inToken = false
		}
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(runes) {
				next := runes[i+1]
				if next == '"' || next == '\\' {
					current.WriteRune(next)
					i++
					continue
				}
			}
			if ch == quote {
				quote = 0
				continue
			}
			current.WriteRune(ch)
			continue
		}

		switch ch {
		case ' ', '\t', '\n', '\r':
			flush()
		case '\'', '"':
			quote = ch
			inToken = true
		case '\\':
			if i+1 < len(runes) {
				i++
				current.WriteRune(runes[i])
				inToken = true
			}
		case '&', '|', '>', '<':
			flush()
			if i+1 < len(runes) && runes[i+1] == ch {
				tokens = append(tokens, string([]rune{ch, ch}))
				i++
			} else {
				tokens = append(tokens, string(ch))
			}
		case ';', '(', ')':
			flush()
			tokens = append(tokens, string(ch))
		default:
			current.WriteRune(ch)
			inToken = true
		}
	}

	if quote != 0 {
		return nil, output.UsageError("unterminated quote in scheduled action")
	}
	flush()
	return tokens, nil
}

// NextWallClockRun computes the next instant after now matching hour:minute in loc.
func NextWallClockRun(now time.Time, hour, minute int, location *time.Location) time.Time {
	localNow := now.In(location)
	candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, location)
	if !candidate.After(localNow) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// ActionExecutor executes one scheduled command's argv in-process.
type ActionExecutor func(ctx context.Context, argv []string) error

// Runner drives one or more enabled schedules until ctx is cancelled or all
// bounded counts complete.
type Runner struct {
	Store   *Store
	Audit   *security.AuditLogger
	Logger  *slog.Logger
	Execute ActionExecutor
	OnEvent func(message string)
}

// Run starts goroutines for all matching enabled schedules and blocks until all
// finish or ctx is cancelled.
func (runner *Runner) Run(ctx context.Context, filterNames []string) error {
	entries, err := runner.Store.Load()
	if err != nil {
		return err
	}

	selected := make([]Entry, 0, len(entries))
	enabledByName := make(map[string]Entry)
	for _, entry := range entries {
		if entry.Enabled {
			enabledByName[entry.Name] = entry
		}
	}

	if len(filterNames) > 0 {
		for _, name := range filterNames {
			entry, ok := enabledByName[name]
			if !ok {
				return output.NewError("not_found", fmt.Sprintf("enabled schedule not found: %s", name))
			}
			selected = append(selected, entry)
		}
	} else {
		for _, entry := range entries {
			if entry.Enabled {
				selected = append(selected, entry)
			}
		}
	}

	if len(selected) == 0 {
		return output.NewError("no_schedules", "no enabled schedules configured")
	}

	if runner.OnEvent != nil {
		runner.OnEvent(fmt.Sprintf("Running %d schedule(s). Ctrl-C to stop.", len(selected)))
	}

	var waitGroup sync.WaitGroup
	for _, entry := range selected {
		waitGroup.Add(1)
		go func(schedule Entry) {
			defer waitGroup.Done()
			runner.runLoop(ctx, schedule)
		}(entry)
	}
	waitGroup.Wait()
	return nil
}

func (runner *Runner) runLoop(ctx context.Context, entry Entry) {
	runs := 0
	for {
		if entry.Count != nil && runs >= *entry.Count {
			return
		}

		var waitDuration time.Duration
		if entry.Time != "" {
			hour, minute, location, _, err := ParseWallClock(entry.Time, entry.TZ)
			if err != nil {
				runner.logFailure(entry.Name, err)
				return
			}
			next := NextWallClockRun(time.Now(), hour, minute, location)
			waitDuration = time.Until(next)
		} else {
			waitDuration = time.Duration(entry.Every) * time.Second
		}

		timer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		runs++
		err := runner.Execute(ctx, slices.Clone(entry.Argv))
		status := "ok"
		if err != nil {
			status = "failed: " + err.Error()
		}

		now := time.Now().UTC()
		_ = runner.Store.UpdateLastRun(entry.Name, status, now)
		if runner.Audit != nil {
			_ = runner.Audit.Record("schedule fire", map[string]any{
				"name":   entry.Name,
				"result": status,
			}, status, "")
		}
		if runner.Logger != nil {
			runner.Logger.Info("schedule fired", "name", entry.Name, "status", status)
		}
		if runner.OnEvent != nil {
			runner.OnEvent(fmt.Sprintf("[%s] %s: %s", now.Format("2006-01-02 15:04:05"), entry.Name, status))
		}
	}
}

func (runner *Runner) logFailure(name string, err error) {
	if runner.Logger != nil {
		runner.Logger.Error("schedule error", "name", name, "error", err)
	}
	if runner.Audit != nil {
		_ = runner.Audit.Record("schedule error", map[string]any{
			"name":  name,
			"error": err.Error(),
		}, "error", "")
	}
}
