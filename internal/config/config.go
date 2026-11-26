// Package config manages vox's persistent configuration file, secure file
// permissions, and Discord bot token resolution.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// EnvConfigDir overrides the base directory where vox stores config,
	// permissions, schedules, and audit logs (default: ~/.vox).
	EnvConfigDir = "VOX_CONFIG_DIR"

	// EnvDiscordBotToken is the primary environment variable for the bot token.
	EnvDiscordBotToken = "DISCORD_BOT_TOKEN" //nolint:gosec // environment variable name, not a secret

	// EnvDiscordToken is the fallback environment variable for the bot token.
	EnvDiscordToken = "DISCORD_TOKEN" //nolint:gosec // environment variable name, not a secret

	// KeyToken is the configuration file key storing the Discord bot token.
	KeyToken = "token"

	// KeyVoice is the configuration file key storing optional voice settings.
	KeyVoice = "voice"
)

// ErrNoToken is returned when no Discord bot token can be resolved from flags,
// environment variables, or the configuration file.
var ErrNoToken = errors.New(
	"no token provided: use --token, set DISCORD_BOT_TOKEN (or DISCORD_TOKEN), or run `vox config set token YOUR_TOKEN`",
)

// TokenSource identifies where a resolved bot token originated.
type TokenSource string

const (
	// TokenSourceFlag indicates the token was provided via --token.
	TokenSourceFlag TokenSource = "flag"
	// TokenSourceBotEnv indicates the token came from DISCORD_BOT_TOKEN.
	TokenSourceBotEnv TokenSource = "DISCORD_BOT_TOKEN" //nolint:gosec // source label, not a secret
	// TokenSourceConfig indicates the token came from ~/.vox/config.json.
	TokenSourceConfig TokenSource = "config"
	// TokenSourceFallbackEnv indicates the token came from DISCORD_TOKEN.
	TokenSourceFallbackEnv TokenSource = "DISCORD_TOKEN"
)

// Store reads and writes vox's configuration directory and files.
type Store struct {
	BaseDir string
}

// NewStore creates a configuration store rooted at baseDir. If baseDir is empty,
// DefaultDir is used.
func NewStore(baseDir string) (*Store, error) {
	if strings.TrimSpace(baseDir) == "" {
		var err error
		baseDir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	return &Store{BaseDir: baseDir}, nil
}

// DefaultDir returns the vox state directory, honoring VOX_CONFIG_DIR before
// defaulting to ~/.vox.
func DefaultDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvConfigDir)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".vox"), nil
}

// ConfigPath returns the absolute path to config.json.
func (store *Store) ConfigPath() string {
	return filepath.Join(store.BaseDir, "config.json")
}

// PermissionsPath returns the absolute path to permissions.json.
func (store *Store) PermissionsPath() string {
	return filepath.Join(store.BaseDir, "permissions.json")
}

// AuditLogPath returns the absolute path to audit.log.
func (store *Store) AuditLogPath() string {
	return filepath.Join(store.BaseDir, "audit.log")
}

// SchedulesPath returns the absolute path to schedules.json.
func (store *Store) SchedulesPath() string {
	return filepath.Join(store.BaseDir, "schedules.json")
}

// DebugCaptureDir returns the default directory for `vox voice capture` WAV files.
func (store *Store) DebugCaptureDir() string {
	return filepath.Join(store.BaseDir, "debug-capture")
}

// Load reads config.json and returns an empty map when the file does not exist.
func (store *Store) Load() (map[string]any, error) {
	data, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", store.ConfigPath(), err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", store.ConfigPath(), err)
	}
	if parsed == nil {
		return map[string]any{}, nil
	}
	return parsed, nil
}

// Save merges updates into config.json and enforces 0700 directory and 0600 file
// permissions on POSIX platforms so stored bot tokens are never world-readable.
func (store *Store) Save(updates map[string]any) error {
	if err := EnsureSecureDir(store.BaseDir); err != nil {
		return err
	}

	existing, err := store.Load()
	if err != nil {
		return err
	}
	maps.Copy(existing, updates)

	payload, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	payload = append(payload, '\n')

	return WriteSecureFile(store.ConfigPath(), payload)
}

// ResolveToken resolves a Discord bot token following vox's precedence order:
//  1. Explicit --token flag
//  2. DISCORD_BOT_TOKEN environment variable
//  3. Stored "token" value in config.json
//  4. DISCORD_TOKEN environment variable
func (store *Store) ResolveToken(flagToken string) (string, TokenSource, error) {
	if trimmed := strings.TrimSpace(flagToken); trimmed != "" {
		return trimmed, TokenSourceFlag, nil
	}
	if envToken := strings.TrimSpace(os.Getenv(EnvDiscordBotToken)); envToken != "" {
		return envToken, TokenSourceBotEnv, nil
	}
	cfg, err := store.Load()
	if err == nil {
		if raw, ok := cfg[KeyToken].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw), TokenSourceConfig, nil
		}
	}
	if fallback := strings.TrimSpace(os.Getenv(EnvDiscordToken)); fallback != "" {
		return fallback, TokenSourceFallbackEnv, nil
	}
	return "", "", ErrNoToken
}

// VoiceConfig returns the optional "voice" object from config.json as a map.
func (store *Store) VoiceConfig() map[string]any {
	cfg, err := store.Load()
	if err != nil {
		return map[string]any{}
	}
	if raw, ok := cfg[KeyVoice].(map[string]any); ok {
		return maps.Clone(raw)
	}
	return map[string]any{}
}

// MaskToken redacts all but the leading 8 characters of a bot token for human output.
func MaskToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "..."
}

// EnsureSecureDir creates dir (including parents) and tightens its mode to 0700 on POSIX.
func EnsureSecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // directory requires 0o700 execute bit
			return fmt.Errorf("chmod directory %s: %w", dir, err)
		}
	}
	return nil
}

// WriteSecureFile writes payload to path with 0600 permissions on POSIX, tightening
// pre-existing files while truncated so secrets never touch disk with broad modes.
func WriteSecureFile(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	if runtime.GOOS != "windows" {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}

	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
