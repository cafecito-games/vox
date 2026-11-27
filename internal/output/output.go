// Package output provides consistent human-readable, JSON, and NDJSON output
// rendering for vox CLI commands while keeping stdout strictly machine-clean.
package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Exit codes returned by the vox binary so scripts and AI agents can branch
// reliably without parsing prose error messages.
const (
	ExitOK               = 0
	ExitError            = 1
	ExitUsage            = 2
	ExitPermissionDenied = 3
	ExitInterrupted      = 130
)

// ChannelVisibilityNote explains Discord's channel visibility filtering.
const ChannelVisibilityNote = "channel listings only include channels this bot can view. " +
	"From 2026-11-16 Discord omits channels without the View Channel permission from the API, " +
	"so a short list may mean missing permissions rather than an empty server."

// CLIError attaches a machine-readable error code and exit status to an error.
type CLIError struct {
	Code     string
	Message  string
	Hint     string
	ExitCode int
	Cause    error
}

// Error implements error.
func (err *CLIError) Error() string {
	return err.Message
}

// Unwrap returns the underlying cause for errors.Is / errors.As.
func (err *CLIError) Unwrap() error {
	return err.Cause
}

// NewError creates a CLIError with ExitError status.
func NewError(code, message string) *CLIError {
	return &CLIError{
		Code:     code,
		Message:  message,
		ExitCode: ExitError,
	}
}

// UsageError creates a CLIError with ExitUsage status (exit code 2).
func UsageError(message string) *CLIError {
	return &CLIError{
		Code:     "usage_error",
		Message:  message,
		ExitCode: ExitUsage,
	}
}

// PermissionError creates a CLIError with ExitPermissionDenied status (exit code 3).
func PermissionError(message string, cause error) *CLIError {
	return &CLIError{
		Code:     "permission_denied",
		Message:  message,
		ExitCode: ExitPermissionDenied,
		Cause:    cause,
	}
}

// InterruptedError creates a CLIError with ExitInterrupted status (exit code 130).
func InterruptedError(message string) *CLIError {
	return &CLIError{
		Code:     "interrupted",
		Message:  message,
		ExitCode: ExitInterrupted,
		Cause:    context.Canceled,
	}
}

// ExitCodeFor returns the process exit code associated with err.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var cliErr *CLIError
	if errors.As(err, &cliErr) && cliErr.ExitCode != 0 {
		return cliErr.ExitCode
	}
	return ExitError
}

// Writer handles formatted output to stdout and diagnostics to stderr.
type Writer struct {
	Stdout  io.Writer
	Stderr  io.Writer
	JSON    bool
	NoColor bool
	IsTTY   bool

	mutex sync.Mutex
}

// NewWriter creates a Writer targeting os.Stdout and os.Stderr with automatic
// TTY and NO_COLOR detection.
func NewWriter(useJSON, noColor bool) *Writer {
	isTTY := term.IsTerminal(int(os.Stdout.Fd())) //nolint:gosec // file descriptor fits in int
	if os.Getenv("NO_COLOR") != "" {
		noColor = true
	}
	return &Writer{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		JSON:    useJSON,
		NoColor: noColor || useJSON || !isTTY,
		IsTTY:   isTTY,
	}
}

// Print emits pretty-printed JSON when JSON mode is active, or plainText otherwise.
func (writer *Writer) Print(data any, plainText string) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	if writer.JSON {
		encoded, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Errorf("encode JSON output: %w", err)
		}
		_, err = fmt.Fprintf(writer.Stdout, "%s\n", encoded)
		return err
	}
	if plainText == "" && data != nil {
		_, err := fmt.Fprintf(writer.Stdout, "%v\n", data)
		return err
	}
	_, err := fmt.Fprintf(writer.Stdout, "%s\n", plainText)
	return err
}

// PrintRaw writes raw text verbatim to stdout without adding an extra newline.
func (writer *Writer) PrintRaw(text string) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	_, err := io.WriteString(writer.Stdout, text)
	return err
}

// PrintStream emits compact single-line NDJSON in JSON mode, or plainText otherwise.
func (writer *Writer) PrintStream(data any, plainText string) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	if writer.JSON {
		encoded, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("encode JSONL output: %w", err)
		}
		_, err = fmt.Fprintf(writer.Stdout, "%s\n", encoded)
		return err
	}
	_, err := fmt.Fprintf(writer.Stdout, "%s\n", plainText)
	return err
}

// PrintJSONL always emits a single compact JSON line to stdout (used by `vox serve`).
func (writer *Writer) PrintJSONL(data any) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode JSONL: %w", err)
	}
	_, err = fmt.Fprintf(writer.Stdout, "%s\n", encoded)
	return err
}

// Note writes a diagnostic note to stderr so stdout remains clean for piping.
func (writer *Writer) Note(format string, args ...any) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	_, _ = fmt.Fprintf(writer.Stderr, format+"\n", args...)
}

// WarnChannelVisibility emits Discord's channel visibility caveat to stderr.
func (writer *Writer) WarnChannelVisibility() {
	writer.Note("note: %s", ChannelVisibilityNote)
}

// PrintError renders err cleanly to stderr. When JSON mode is active, a
// structured JSON error object is written to stderr so agents get machine-readable diagnostics.
func (writer *Writer) PrintError(err error) {
	if err == nil {
		return
	}
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	code := "error"
	hint := ""
	message := err.Error()
	var cliErr *CLIError
	if errors.As(err, &cliErr) {
		if cliErr.Code != "" {
			code = cliErr.Code
		}
		hint = cliErr.Hint
	}

	if writer.JSON {
		payload := map[string]any{
			"error": message,
			"code":  code,
		}
		if hint != "" {
			payload["hint"] = hint
		}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr == nil {
			_, _ = fmt.Fprintf(writer.Stderr, "%s\n", encoded)
			return
		}
	}

	if code == "interrupted" {
		_, _ = fmt.Fprintln(writer.Stderr, message)
		return
	}

	prefix := "Error:"
	if !writer.NoColor {
		prefix = "\x1b[31mError:\x1b[0m"
	}
	_, _ = fmt.Fprintf(writer.Stderr, "%s %s\n", prefix, message)
	if strings.TrimSpace(hint) != "" {
		_, _ = fmt.Fprintf(writer.Stderr, "Hint: %s\n", hint)
	}
}
