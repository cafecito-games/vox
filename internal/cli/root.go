// Package cli wires vox's Cobra command tree, global flags, permission
// profile enforcement, structured logging, and output formatting.
//
// Command handlers in this package stay thin: they parse arguments, enforce
// security rules, invoke internal domain packages, and render results via
// internal/output.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/disgoorg/disgo/rest"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/cafecito-games/vox/internal/buildinfo"
	"github.com/cafecito-games/vox/internal/config"
	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/logging"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
)

// App holds shared runtime dependencies and global flags for a CLI invocation.
type App struct {
	Token       string
	UseJSON     bool
	Yes         bool
	Profile     string
	LogLevel    string
	LogFormat   string
	NoColor     bool
	ConfigDir   string
	ShowVersion bool

	In          io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive *bool

	Store    *config.Store
	Security *security.Manager
	Out      *output.Writer
	Logger   *slog.Logger
	Root     *cobra.Command
}

// NewApp returns an App wired to standard OS I/O streams.
func NewApp() *App {
	return &App{
		In:     os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

// Execute constructs the root command tree, executes it against os.Args, and
// returns a process exit code.
func Execute(ctx context.Context) int {
	app := NewApp()
	cmd := NewRootCommand(app)
	cmd.SetContext(ctx)

	if err := cmd.Execute(); err != nil {
		if app.Out == nil {
			app.Out = output.NewWriter(app.UseJSON, app.NoColor)
		}
		app.Out.PrintError(err)
		return output.ExitCodeFor(err)
	}
	return output.ExitOK
}

// NewRootCommand builds the complete vox Cobra command tree.
func NewRootCommand(app *App) *cobra.Command {
	if app == nil {
		app = NewApp()
	}

	root := &cobra.Command{
		Use:   "vox",
		Short: "Discord CLI for humans and AI agents",
		Long: "vox — Discord CLI for humans and AI agents.\n\n" +
			"One-shot commands talk directly to Discord's HTTP API without opening a Gateway\n" +
			"session. Gateway connections are reserved for live commands (`listen`, `serve`,\n" +
			"`voice`) and request only the minimal intents needed.\n\n" +
			"Every command supports `--json` for machine-readable output. Run `vox schema --json`\n" +
			"to inspect the full CLI command tree and `vox serve` JSONL protocol.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return app.initRuntime()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if app.ShowVersion {
				info := buildinfo.Current()
				return app.Out.Print(info, info.String())
			}
			return cmd.Help()
		},
	}

	root.SetIn(app.In)
	root.SetOut(app.Stdout)
	root.SetErr(app.Stderr)

	flags := root.PersistentFlags()
	flags.StringVar(&app.Token, "token", "", "Discord bot token (or DISCORD_BOT_TOKEN / config.json / DISCORD_TOKEN)")
	flags.BoolVar(&app.UseJSON, "json", false, "Emit machine-readable JSON output")
	flags.BoolVarP(&app.Yes, "yes", "y", false, "Skip confirmation prompts for destructive actions")
	flags.StringVar(&app.Profile, "profile", "", "Override active permission profile (full, chat, readonly, moderation, or custom)")
	flags.StringVar(&app.LogLevel, "log-level", "", "Structured log level on stderr (debug, info, warn, error)")
	flags.StringVar(&app.LogFormat, "log-format", "", "Structured log format on stderr (text, json)")
	flags.BoolVar(&app.NoColor, "no-color", false, "Disable ANSI color formatting")
	flags.StringVar(&app.ConfigDir, "config-dir", "", "Override vox configuration directory (default: ~/.vox)")
	root.Flags().BoolVarP(&app.ShowVersion, "version", "V", false, "Print version information and exit")

	root.AddGroup(
		&cobra.Group{ID: "messaging", Title: "Messaging & Reactions:"},
		&cobra.Group{ID: "server", Title: "Server, Channels & Members:"},
		&cobra.Group{ID: "moderation", Title: "Moderation & Community:"},
		&cobra.Group{ID: "realtime", Title: "Live Events, Serve, Interact & Voice:"},
		&cobra.Group{ID: "admin", Title: "Setup, Security & Introspection:"},
	)

	app.Root = root

	// Register all command groups.
	root.AddCommand(
		// Messaging
		newMessageCommand(app),
		newDMCommand(app),
		newReactionCommand(app),
		newTypingCommand(app),
		newPollCommand(app),

		// Server, Channels & Members
		newChannelCommand(app),
		newThreadCommand(app),
		newServerCommand(app),
		newRoleCommand(app),
		newMemberCommand(app),
		newWebhookCommand(app),

		// Moderation & Community
		newEventCommand(app),
		newInviteCommand(app),
		newEmojiCommand(app),
		newAutoModCommand(app),

		// Real-time, Automation & Voice
		newListenCommand(app),
		newServeCommand(app),
		newScheduleCommand(app),
		newInteractCommand(app),
		newVoiceCommand(app),

		// Setup, Security & Introspection
		newConfigCommand(app),
		newPermissionCommand(app),
		newAuditCommand(app),
		newDoctorCommand(app),
		newSetupCommand(app),
		newSchemaCommand(app),
		newVersionCommand(app),
	)

	return root
}

func (app *App) initRuntime() error {
	store, err := config.NewStore(app.ConfigDir)
	if err != nil {
		return err
	}
	app.Store = store
	app.Security = security.NewManager(store)

	isTTY := app.isStdoutTTY()
	noColor := app.NoColor || app.UseJSON || !isTTY || os.Getenv("NO_COLOR") != ""
	app.Out = &output.Writer{
		Stdout:  app.Stdout,
		Stderr:  app.Stderr,
		JSON:    app.UseJSON,
		NoColor: noColor,
		IsTTY:   isTTY,
	}

	logger, err := logging.New(logging.Options{
		Level:  app.LogLevel,
		Format: app.LogFormat,
		Writer: app.Stderr,
	})
	if err != nil {
		return output.UsageError(err.Error())
	}
	app.Logger = logger
	return nil
}

func (app *App) isStdoutTTY() bool {
	if app.Interactive != nil {
		return *app.Interactive
	}
	if file, ok := app.Stdout.(*os.File); ok {
		return term.IsTerminal(int(file.Fd())) //nolint:gosec // file descriptor fits in int
	}
	return false
}

func (app *App) isInteractive() bool {
	if app.Interactive != nil {
		return *app.Interactive
	}
	inFile, inOK := app.In.(*os.File)
	outFile, outOK := app.Stdout.(*os.File)
	if !inOK || !outOK {
		return false
	}
	return term.IsTerminal(int(inFile.Fd())) && term.IsTerminal(int(outFile.Fd())) //nolint:gosec // file descriptors fit in int
}

// Enforce verifies that cmd is permitted by the active permission profile.
func (app *App) Enforce(cmd *cobra.Command) error {
	if err := app.Security.Enforce(cmd.CommandPath(), app.Profile); err != nil {
		if errors.Is(err, security.ErrPermissionDenied) {
			return output.PermissionError(err.Error(), err)
		}
		return output.UsageError(err.Error())
	}
	return nil
}

// ConfirmDestructive enforces the permission profile first, then prompts (unless --yes).
func (app *App) ConfirmDestructive(commandPath, details string) error {
	err := app.Security.ConfirmDestructive(security.ConfirmOptions{
		CommandPath:     commandPath,
		Details:         details,
		ProfileOverride: app.Profile,
		SkipConfirm:     app.Yes,
		Interactive:     app.isInteractive(),
		In:              app.In,
		Out:             app.Stderr,
	})
	if err != nil {
		if errors.Is(err, security.ErrPermissionDenied) {
			return output.PermissionError(err.Error(), err)
		}
		return output.NewError("aborted", err.Error())
	}
	return nil
}

// ResolveToken enforces the permission profile and resolves the Discord bot token.
func (app *App) ResolveToken(cmd *cobra.Command) (string, error) {
	if err := app.Enforce(cmd); err != nil {
		return "", err
	}
	token, _, err := app.Store.ResolveToken(app.Token)
	if err != nil {
		return "", &output.CLIError{
			Code:     "missing_token",
			Message:  err.Error(),
			Hint:     "Run `vox setup` interactively, `vox config set token <TOKEN>`, or set DISCORD_BOT_TOKEN.",
			ExitCode: output.ExitError,
			Cause:    err,
		}
	}
	return token, nil
}

// WithRest enforces permissions, opens an HTTP-only Discord REST client, runs fn,
// and closes the client cleanly.
func (app *App) WithRest(cmd *cobra.Command, fn func(rest.Rest) error) error {
	token, err := app.ResolveToken(cmd)
	if err != nil {
		return err
	}
	client := voxdiscord.NewRest(token, app.Logger)
	defer client.Close(context.Background())
	return fn(client)
}

// WaitRateLimit enforces the sliding-window moderation rate limiter and prints
// any wait notice to stderr.
func (app *App) WaitRateLimit() {
	app.Security.Limiter.Wait(func(d time.Duration) {
		app.Out.Note("Rate limited. Waiting %.1fs...", d.Seconds())
	})
}

// ValidateCommandArgv checks that a scheduled command's argv matches a real
// command path in vox's Cobra tree.
func (app *App) ValidateCommandArgv(argv []string) error {
	if app.Root == nil || len(argv) == 0 {
		return output.UsageError("empty scheduled command")
	}
	node := app.Root
	for _, token := range argv {
		if strings.HasPrefix(token, "-") {
			break
		}
		if !node.HasSubCommands() {
			break
		}
		var next *cobra.Command
		for _, sub := range node.Commands() {
			if sub.Name() == token || sub.HasAlias(token) {
				next = sub
				break
			}
		}
		if next == nil {
			names := make([]string, 0, len(node.Commands()))
			for _, sub := range node.Commands() {
				names = append(names, sub.Name())
			}
			return output.UsageError(fmt.Sprintf(
				"unknown command %q under %q (available: %s)",
				token,
				node.CommandPath(),
				strings.Join(names, ", "),
			))
		}
		node = next
	}
	if node == app.Root || node.RunE == nil {
		return output.UsageError(fmt.Sprintf("scheduled action %q does not resolve to a runnable subcommand", strings.Join(argv, " ")))
	}
	return nil
}

func newVersionCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print build and version information",
		GroupID: "admin",
		RunE: func(_ *cobra.Command, _ []string) error {
			info := buildinfo.Current()
			return app.Out.Print(info, info.String())
		},
	}
}
