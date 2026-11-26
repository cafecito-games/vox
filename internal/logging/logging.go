// Package logging configures structured log/slog loggers for vox.
//
// All log records are emitted to stderr so stdout stays strictly reserved for
// command output, --json payloads, and serve/listen JSONL streams.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const (
	// EnvLogLevel overrides the default structured log level.
	EnvLogLevel = "VOX_LOG_LEVEL"
	// EnvLogFormat overrides the default structured log format ("text" or "json").
	EnvLogFormat = "VOX_LOG_FORMAT"
)

// Options controls how a structured logger is constructed.
type Options struct {
	Level  string
	Format string
	Writer io.Writer
}

// New creates a configured *slog.Logger using explicit options, environment
// defaults, and sensible fallbacks (warn level, text format, os.Stderr).
func New(options Options) (*slog.Logger, error) {
	writer := options.Writer
	if writer == nil {
		writer = os.Stderr
	}

	levelText := strings.TrimSpace(options.Level)
	if levelText == "" {
		levelText = strings.TrimSpace(os.Getenv(EnvLogLevel))
	}
	if levelText == "" {
		levelText = "warn"
	}

	level, err := ParseLevel(levelText)
	if err != nil {
		return nil, err
	}

	formatText := strings.TrimSpace(options.Format)
	if formatText == "" {
		formatText = strings.TrimSpace(os.Getenv(EnvLogFormat))
	}
	if formatText == "" {
		formatText = "text"
	}

	handlerOptions := &slog.HandlerOptions{
		Level: level,
	}

	switch strings.ToLower(formatText) {
	case "text":
		return slog.New(slog.NewTextHandler(writer, handlerOptions)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(writer, handlerOptions)), nil
	default:
		return nil, fmt.Errorf("invalid log format %q: must be \"text\" or \"json\"", formatText)
	}
}

// ParseLevel converts a case-insensitive level name into a slog.Level.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelWarn, fmt.Errorf("invalid log level %q: must be debug, info, warn, or error", value)
	}
}

// Discard returns a logger that drops all records, useful for tests.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
