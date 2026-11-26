// Package doctor diagnoses local vox installation health, voice/provider
// readiness, and (when --server is given) live Discord permission bitfields
// including Discord's 2026 permission splits.
package doctor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"

	"github.com/cafecito-games/vox/internal/buildinfo"
	"github.com/cafecito-games/vox/internal/config"
	voxdiscord "github.com/cafecito-games/vox/internal/discord"
)

// Check represents one diagnostic assertion.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Skipped bool   `json:"skipped"`
}

// Section groups related diagnostic checks.
type Section struct {
	Name   string  `json:"name"`
	Checks []Check `json:"checks"`
}

// Report is the complete diagnostic result returned by Run.
type Report struct {
	Sections []Section `json:"sections"`
	Failures int       `json:"failures"`
	Skipped  int       `json:"skipped"`
}

// Options configures a doctor diagnostic run.
type Options struct {
	Store     *config.Store
	FlagToken string
	Server    string
	Logger    *slog.Logger
	LookPath  func(string) (string, error)
	Getenv    func(string) string
}

// Run executes all local checks and, when options.Server is non-empty,
// queries Discord over REST to verify the bot's real guild permission bitfield.
func Run(options Options) Report {
	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	token, source, tokenErr := options.Store.ResolveToken(options.FlagToken)
	info := buildinfo.Current()

	// 1. CORE section.
	coreChecks := []Check{
		{
			Name:   "vox binary",
			OK:     true,
			Detail: fmt.Sprintf("v%s (%s)", info.Version, runtime.Version()),
		},
	}
	if tokenErr == nil {
		coreChecks = append(coreChecks, Check{
			Name:   "bot token",
			OK:     true,
			Detail: fmt.Sprintf("configured (%s)", source),
		})
	} else {
		coreChecks = append(coreChecks, Check{
			Name:   "bot token",
			OK:     false,
			Detail: "not configured",
			Hint:   "run `vox config set token <TOKEN>` or set DISCORD_BOT_TOKEN / DISCORD_TOKEN",
		})
	}

	// 2. VOICE section (pure-Go DAVE + Opus compiled in, plus optional ffmpeg check).
	ffmpegPath, ffmpegErr := lookPath("ffmpeg")
	voiceChecks := []Check{
		{
			Name:   "pure-Go DAVE E2EE",
			OK:     true,
			Detail: "built-in (dave-go + godave, CGO-free)",
		},
		{
			Name:   "pure-Go Opus decode",
			OK:     true,
			Detail: "built-in (pion/opus, CGO-free)",
		},
	}
	if ffmpegErr == nil {
		voiceChecks = append(voiceChecks, Check{
			Name:   "ffmpeg",
			OK:     true,
			Detail: ffmpegPath,
		})
	} else {
		voiceChecks = append(voiceChecks, Check{
			Name:    "ffmpeg",
			OK:      false,
			Detail:  "not on PATH (optional, required for voice playback/TTS)",
			Hint:    "install ffmpeg (`brew install ffmpeg` or `apt install ffmpeg`) for `vox voice play` and `vox voice speak`",
			Skipped: true,
		})
	}

	// 3. STT & TTS provider key checks (optional unless configured).
	sttChecks := []Check{
		envKeyCheck("DEEPGRAM_API_KEY", getenv("DEEPGRAM_API_KEY"), "set DEEPGRAM_API_KEY for streaming Deepgram STT"),
		envKeyCheck("OPENAI_API_KEY (Whisper)", getenv("OPENAI_API_KEY"), "set OPENAI_API_KEY for OpenAI Whisper STT"),
	}
	ttsChecks := []Check{
		envKeyCheck("ELEVENLABS_API_KEY", getenv("ELEVENLABS_API_KEY"), "set ELEVENLABS_API_KEY for ElevenLabs TTS"),
		envKeyCheck("OPENAI_API_KEY (TTS)", getenv("OPENAI_API_KEY"), "set OPENAI_API_KEY for OpenAI TTS"),
		envKeyCheck("DEEPGRAM_API_KEY (Aura)", getenv("DEEPGRAM_API_KEY"), "set DEEPGRAM_API_KEY for Deepgram Aura TTS"),
	}

	// 4. PERMISSIONS section (offline unless --server is passed).
	permChecks := checkGuildPermissions(options.Server, token, options.Logger)

	sections := []Section{
		{Name: "CORE", Checks: coreChecks},
		{Name: "VOICE", Checks: voiceChecks},
		{Name: "STT", Checks: sttChecks},
		{Name: "TTS", Checks: ttsChecks},
		{Name: "PERMISSIONS", Checks: permChecks},
	}

	report := Report{Sections: sections}
	for _, section := range sections {
		for _, check := range section.Checks {
			if check.Skipped {
				report.Skipped++
			} else if !check.OK {
				report.Failures++
			}
		}
	}
	return report
}

func envKeyCheck(label, value, hint string) Check {
	if strings.TrimSpace(value) != "" {
		return Check{Name: label, OK: true, Detail: "configured"}
	}
	return Check{Name: label, OK: false, Detail: "not set", Hint: hint, Skipped: true}
}

func checkGuildPermissions(server, token string, logger *slog.Logger) []Check {
	if strings.TrimSpace(server) == "" {
		return []Check{{
			Name:    "guild permissions",
			OK:      false,
			Detail:  "not checked",
			Hint:    "pass --server <name-or-id> to verify the bot's live Discord permission bitfield",
			Skipped: true,
		}}
	}
	if strings.TrimSpace(token) == "" {
		return []Check{{
			Name:   "guild permissions",
			OK:     false,
			Detail: "no token configured",
			Hint:   "configure a bot token before checking server permissions",
		}}
	}

	restClient := voxdiscord.NewRest(token, logger)
	defer restClient.Close(context.Background())

	me, err := restClient.GetCurrentUser("")
	if err != nil {
		return []Check{{
			Name:   "guild permissions",
			OK:     false,
			Detail: fmt.Sprintf("lookup failed: %v", voxdiscord.MapError(err)),
		}}
	}
	guild, err := voxdiscord.ResolveGuild(restClient, server)
	if err != nil {
		return []Check{{
			Name:   "guild permissions",
			OK:     false,
			Detail: fmt.Sprintf("lookup failed: %v", err),
		}}
	}
	perms, err := restClient.GetGuildMemberPermissions(guild.ID, me.ID)
	if err != nil {
		return []Check{{
			Name:   "guild permissions",
			OK:     false,
			Detail: fmt.Sprintf("lookup failed: %v", voxdiscord.MapError(err)),
		}}
	}

	return EvaluateGuildPermissions(guild.Name, guild.ID.String(), perms)
}

// EvaluateGuildPermissions inspects a bot's live guild permission bitfield for
// administrator access, 2026 Discord permission-split regressions, and coverage
// across vox's 28 operational permissions.
func EvaluateGuildPermissions(guildName, guildID string, perms disgodiscord.Permissions) []Check {
	checks := []Check{
		{Name: "guild", OK: true, Detail: fmt.Sprintf("%s (ID: %s)", guildName, guildID)},
	}
	if perms.Has(disgodiscord.PermissionAdministrator) {
		checks = append(checks, Check{
			Name:   "administrator",
			OK:     true,
			Detail: "granted — every permission is implied",
		})
		return checks
	}

	regressions := voxdiscord.DetectPermissionSplits(perms)
	if len(regressions) > 0 {
		parts := make([]string, 0, len(regressions))
		affected := make([]string, 0, len(regressions))
		for _, split := range regressions {
			parts = append(parts, fmt.Sprintf("%s (covered by %s until %s)", split.NewBit, split.LegacyBit, split.EnforcedSince))
			affected = append(affected, split.Affected)
		}
		checks = append(checks, Check{
			Name:   "permission splits",
			OK:     false,
			Detail: strings.Join(parts, "; "),
			Hint:   "re-invite the bot with updated permissions (affected: " + strings.Join(affected, "; ") + ")",
		})
	} else {
		checks = append(checks, Check{
			Name:   "permission splits",
			OK:     true,
			Detail: "no regressions",
		})
	}

	granted := 0
	var missing []string
	for _, permName := range voxdiscord.VoxPermissions {
		bit := voxdiscord.PermissionNameMap[permName]
		if perms.Has(bit) {
			granted++
		} else {
			missing = append(missing, permName)
		}
	}
	detail := fmt.Sprintf("%d/%d granted", granted, len(voxdiscord.VoxPermissions))
	if len(missing) > 0 {
		detail += " — not granted: " + strings.Join(missing, ", ")
	}
	checks = append(checks, Check{
		Name:   "vox permissions",
		OK:     true,
		Detail: detail,
	})
	return checks
}

// FormatText renders a Report into a concise human-readable terminal summary.
func FormatText(report Report) string {
	var builder strings.Builder
	for i, section := range report.Sections {
		if i > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "%s\n", section.Name)
		for _, check := range section.Checks {
			marker := "[ok]"
			if check.Skipped {
				marker = "[--]"
			} else if !check.OK {
				marker = "[FAIL]"
			}
			line := fmt.Sprintf("  %-6s %s", marker, check.Name)
			if check.Detail != "" {
				line += " — " + check.Detail
			}
			builder.WriteString(line + "\n")
			if check.Hint != "" && !check.OK {
				builder.WriteString("        hint: " + check.Hint + "\n")
			}
		}
	}
	if report.Failures == 0 {
		fmt.Fprintf(&builder, "\nOK — no problems found (%d optional check(s) skipped).", report.Skipped)
	} else {
		fmt.Fprintf(&builder, "\n%d problem(s) found. See hints above.", report.Failures)
	}
	return builder.String()
}
