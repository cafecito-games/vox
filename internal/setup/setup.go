// Package setup implements the interactive human-first `vox setup` wizard.
//
// The wizard refuses to run when stdin/stdout are non-interactive or --json is
// active, guiding scripts and AI agents to use individual non-interactive
// commands instead.
package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"golang.org/x/term"

	"github.com/cafecito-games/vox/internal/config"
	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/doctor"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
)

// NonInteractiveHelp explains how scripts and AI agents should configure vox.
const NonInteractiveHelp = `vox setup requires an interactive terminal (it cannot run with piped stdin or --json).

For scripts and AI agents, configure vox directly:
  vox config set token <YOUR_BOT_TOKEN>
  vox permission set <full|chat|readonly|moderation>
  vox doctor --json`

// BotIdentity captures the result of validating a bot token against Discord REST.
type BotIdentity struct {
	ID      snowflake.ID
	Name    string
	Guilds  []string
	HasMore bool
}

// TokenValidator verifies a token against Discord and returns bot metadata.
type TokenValidator func(token string) (BotIdentity, error)

// Options configures an interactive setup run.
type Options struct {
	Store         *config.Store
	Security      *security.Manager
	Logger        *slog.Logger
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	Interactive   bool
	UseJSON       bool
	ValidateToken TokenValidator
	ReadSecret    func(prompt string) (string, error)
}

// Run executes the 5-step interactive setup wizard.
func Run(options Options) error {
	if options.UseJSON || !options.Interactive {
		if options.Err != nil {
			_, _ = fmt.Fprintln(options.Err, NonInteractiveHelp)
		}
		return output.NewError("non_interactive", "vox setup requires an interactive terminal")
	}

	in := options.In
	if in == nil {
		in = os.Stdin
	}
	out := options.Out
	if out == nil {
		out = os.Stdout
	}
	errOut := options.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	validator := options.ValidateToken
	if validator == nil {
		validator = func(token string) (BotIdentity, error) {
			return defaultValidateToken(token, options.Logger)
		}
	}

	reader := bufio.NewReader(in)
	readLine := func(prompt string) (string, error) {
		_, _ = fmt.Fprint(out, prompt)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	confirm := func(prompt string, defaultYes bool) (bool, error) {
		suffix := " [y/N]: "
		if defaultYes {
			suffix = " [Y/n]: "
		}
		answer, err := readLine(prompt + suffix)
		if err != nil {
			return false, err
		}
		if answer == "" {
			return defaultYes, nil
		}
		lower := strings.ToLower(answer)
		return lower == "y" || lower == "yes", nil
	}
	readSecret := options.ReadSecret
	if readSecret == nil {
		readSecret = func(prompt string) (string, error) {
			_, _ = fmt.Fprint(out, prompt)
			if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) { //nolint:gosec // file descriptor fits in int
				raw, err := term.ReadPassword(int(file.Fd())) //nolint:gosec // file descriptor fits in int
				_, _ = fmt.Fprintln(out)
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(string(raw)), nil
			}
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return "", err
			}
			return strings.TrimSpace(line), nil
		}
	}

	_, _ = fmt.Fprintln(out, "vox setup — press Ctrl-C at any point to stop.")
	_, _ = fmt.Fprintln(out)

	// Step 1: Bot token.
	_, _ = fmt.Fprintln(out, "1. Bot token")
	if envTok := strings.TrimSpace(os.Getenv(config.EnvDiscordBotToken)); envTok != "" {
		_, _ = fmt.Fprintln(errOut, "   Note: DISCORD_BOT_TOKEN is set in your environment and takes precedence over config.json.")
	}
	storedCfg, _ := options.Store.Load()
	storedToken, _ := storedCfg[config.KeyToken].(string)
	existingToken, _, _ := options.Store.ResolveToken("")

	var verified BotIdentity
	var verifiedToken string
	useExisting := false
	if existingToken != "" {
		var err error
		useExisting, err = confirm("   Use the token already configured?", true)
		if err != nil {
			return err
		}
	}

	for attempt := range 3 {
		candidate := ""
		if attempt == 0 && useExisting && existingToken != "" {
			candidate = existingToken
		} else {
			var err error
			candidate, err = readSecret("   Discord bot token: ")
			if err != nil {
				return err
			}
		}
		if candidate == "" {
			_, _ = fmt.Fprintln(errOut, "   Token cannot be empty.")
			useExisting = false
			continue
		}
		identity, err := validator(candidate)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "   Token validation failed: %v\n", err)
			useExisting = false
			continue
		}
		verified = identity
		verifiedToken = candidate
		break
	}
	if verifiedToken == "" {
		return output.NewError(
			"invalid_token",
			"No working bot token. Create one at https://discord.com/developers/applications and run `vox setup` again.",
		)
	}

	guildSummary := "none yet"
	if len(verified.Guilds) > 0 {
		guildSummary = strings.Join(verified.Guilds, ", ")
		if verified.HasMore {
			guildSummary += ", and more"
		}
	}
	_, _ = fmt.Fprintf(out, "   Authenticated as %s (ID: %s)\n", verified.Name, verified.ID)
	_, _ = fmt.Fprintf(out, "   Servers: %s\n", guildSummary)

	if verifiedToken != storedToken {
		if err := options.Store.Save(map[string]any{config.KeyToken: verifiedToken}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "   Saved token to %s (mode 0600).\n", options.Store.ConfigPath())
	}
	_, _ = fmt.Fprintln(out)

	// Step 2: Invite URL builder.
	_, _ = fmt.Fprintln(out, "2. Invite the bot to a server")
	var selectedPerms disgodiscord.Permissions
	for _, bundle := range voxdiscord.InviteBundles {
		ok, err := confirm(fmt.Sprintf("   Include %s (%s)?", bundle.Name, bundle.Description), bundle.Default)
		if err != nil {
			return err
		}
		if ok {
			mask, _ := voxdiscord.ParsePermissionNames(bundle.Permissions)
			selectedPerms = selectedPerms.Add(mask)
		}
	}
	if selectedPerms != 0 {
		inviteURL := voxdiscord.BuildOAuthInviteURL(verified.ID, selectedPerms)
		_, _ = fmt.Fprintf(out, "   Invite URL:\n   %s\n", inviteURL)
		_, _ = fmt.Fprintln(out, "   Note: Enable Message Content or Server Members in Developer Portal > Bot only if your commands need them.")
	} else {
		_, _ = fmt.Fprintln(out, "   Skipped invite URL generation.")
	}
	_, _ = fmt.Fprintln(out)

	// Step 3: Permission profile.
	_, _ = fmt.Fprintln(out, "3. Permission profile")
	profiles := options.Security.Profiles()
	currentProfile := options.Security.ActiveProfileName()
	names := slices.Sorted(maps.Keys(profiles))
	for _, name := range names {
		marker := " "
		if name == currentProfile {
			marker = "*"
		}
		_, _ = fmt.Fprintf(out, "   %s %-12s %s\n", marker, name, profiles[name].Description)
	}
	chosenProfile, err := readLine(fmt.Sprintf("   Active profile [%s]: ", currentProfile))
	if err != nil {
		return err
	}
	if chosenProfile == "" {
		chosenProfile = currentProfile
	}
	if _, err := options.Security.SetActiveProfile(chosenProfile); err != nil {
		return err
	}
	if chosenProfile != "full" {
		_, _ = fmt.Fprintln(out, "   Note: Re-running setup later requires `vox --profile full setup`.")
	}
	_, _ = fmt.Fprintln(out)

	// Step 4: Voice providers (optional).
	_, _ = fmt.Fprintln(out, "4. Voice (optional)")
	configureVoice, err := confirm("   Configure TTS/STT voice providers?", false)
	if err != nil {
		return err
	}
	if configureVoice {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			_, _ = fmt.Fprintln(errOut, "   Warning: ffmpeg was not found on PATH; install ffmpeg for voice playback/TTS.")
		}
		ttsChoice, err := readLine("   TTS provider (elevenlabs/openai/deepgram/none) [elevenlabs]: ")
		if err != nil {
			return err
		}
		if ttsChoice == "" {
			ttsChoice = "elevenlabs"
		}
		sttChoice, err := readLine("   STT provider (deepgram/openai/none) [deepgram]: ")
		if err != nil {
			return err
		}
		if sttChoice == "" {
			sttChoice = "deepgram"
		}
		printVoiceEnvHints(out, ttsChoice, sttChoice)
	}
	_, _ = fmt.Fprintln(out)

	// Step 5: Verify via offline doctor.
	_, _ = fmt.Fprintln(out, "5. Verify")
	report := doctor.Run(doctor.Options{
		Store:  options.Store,
		Logger: options.Logger,
	})
	_, _ = fmt.Fprintln(out, doctor.FormatText(report))
	if report.Failures > 0 {
		return output.NewError("doctor_failed", fmt.Sprintf("%d setup check(s) failed", report.Failures))
	}
	_, _ = fmt.Fprintln(out, "\n   Ready. Try: vox server list")
	return nil
}

func printVoiceEnvHints(out io.Writer, ttsChoice, sttChoice string) {
	providerEnv := map[string]string{
		"elevenlabs": "ELEVENLABS_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"deepgram":   "DEEPGRAM_API_KEY",
	}
	useWindows := runtime.GOOS == "windows" && os.Getenv("MSYSTEM") == "" && os.Getenv("SHELL") == ""
	formatEnv := func(name, value string) string {
		if useWindows {
			return fmt.Sprintf(`   setx %s "%s"`, name, value)
		}
		return fmt.Sprintf("   export %s=%q", name, value)
	}
	_, _ = fmt.Fprintln(out, "   Add these environment variables to your shell profile:")
	if ttsChoice != "none" {
		_, _ = fmt.Fprintln(out, formatEnv("VOX_TTS", ttsChoice))
		if keyVar, ok := providerEnv[strings.ToLower(ttsChoice)]; ok && os.Getenv(keyVar) == "" {
			_, _ = fmt.Fprintln(out, formatEnv(keyVar, "<YOUR_API_KEY>"))
		}
	}
	if sttChoice != "none" {
		_, _ = fmt.Fprintln(out, formatEnv("VOX_STT", sttChoice))
		if keyVar, ok := providerEnv[strings.ToLower(sttChoice)]; ok && os.Getenv(keyVar) == "" {
			_, _ = fmt.Fprintln(out, formatEnv(keyVar, "<YOUR_API_KEY>"))
		}
	}
}

func defaultValidateToken(token string, logger *slog.Logger) (BotIdentity, error) {
	restClient := voxdiscord.NewRest(token, logger)
	defer restClient.Close(context.Background())

	user, err := restClient.GetCurrentUser("")
	if err != nil {
		return BotIdentity{}, voxdiscord.MapError(err)
	}
	guilds, err := restClient.GetCurrentUserGuilds("", 0, 0, 11, 0, false)
	if err != nil {
		return BotIdentity{}, voxdiscord.MapError(err)
	}
	hasMore := len(guilds) > 10
	if hasMore {
		guilds = guilds[:10]
	}
	names := make([]string, len(guilds))
	for i, guild := range guilds {
		names[i] = guild.Name
	}
	return BotIdentity{
		ID:      user.ID,
		Name:    user.Username,
		Guilds:  names,
		HasMore: hasMore,
	}, nil
}
