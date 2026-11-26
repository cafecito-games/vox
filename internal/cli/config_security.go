package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cafecito-games/vox/internal/config"
	"github.com/cafecito-games/vox/internal/doctor"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
	"github.com/cafecito-games/vox/internal/setup"
)

func newConfigCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "config",
		Short:   "Manage vox configuration (~/.vox/config.json)",
		GroupID: "admin",
	}

	group.AddCommand(
		&cobra.Command{
			Use:   "set <key> <value>",
			Short: "Set a configuration value",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := app.Enforce(cmd); err != nil {
					return err
				}
				key, value := args[0], args[1]
				if err := app.Store.Save(map[string]any{key: value}); err != nil {
					return err
				}
				return app.Out.Print(map[string]any{"key": key, "set": true}, fmt.Sprintf("Set %s.", key))
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Display current configuration (token redacted in plain text)",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				data, err := app.Store.Load()
				if err != nil {
					return err
				}
				if len(data) == 0 {
					return app.Out.Print(data, "No configuration set.")
				}
				keys := slices.Sorted(maps.Keys(data))
				lines := make([]string, 0, len(keys))
				for _, key := range keys {
					val := data[key]
					display := fmt.Sprint(val)
					if key == config.KeyToken {
						if tokenStr, ok := val.(string); ok {
							display = config.MaskToken(tokenStr)
						}
					}
					lines = append(lines, fmt.Sprintf("%s: %s", key, display))
				}
				return app.Out.Print(data, strings.Join(lines, "\n"))
			},
		},
	)
	return group
}

func newPermissionCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "permission",
		Short:   "Manage command access control profiles",
		GroupID: "admin",
	}

	group.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show the active permission profile",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				active := app.Security.ActiveProfileName()
				profiles := app.Security.Profiles()
				profile, ok := profiles[active]
				if !ok {
					profile = security.DefaultProfiles()["full"]
				}
				payload := map[string]any{
					"active_profile": active,
					"description":    profile.Description,
					"allowed":        profile.Allowed,
					"denied":         profile.Denied,
				}
				plain := strings.Join([]string{
					fmt.Sprintf("Active profile: %s", active),
					fmt.Sprintf("Description: %s", profile.Description),
					fmt.Sprintf("Allowed: %s", strings.Join(profile.Allowed, ", ")),
					fmt.Sprintf("Denied: %s", strings.Join(profile.Denied, ", ")),
				}, "\n")
				return app.Out.Print(payload, plain)
			},
		},
		&cobra.Command{
			Use:   "set <profile>",
			Short: "Set the active permission profile (full, chat, readonly, moderation, or custom)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := app.Enforce(cmd); err != nil {
					return err
				}
				name := args[0]
				profile, err := app.Security.SetActiveProfile(name)
				if err != nil {
					return output.UsageError(err.Error())
				}
				payload := map[string]any{
					"active_profile": name,
					"description":    profile.Description,
				}
				return app.Out.Print(payload, fmt.Sprintf("Permission profile set to: %s (%s)", name, profile.Description))
			},
		},
		&cobra.Command{
			Use:   "profiles",
			Short: "List all available permission profiles",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				profiles := app.Security.Profiles()
				names := slices.Sorted(maps.Keys(profiles))
				lines := make([]string, 0, len(names))
				for _, name := range names {
					lines = append(lines, fmt.Sprintf("  %-12s %s", name, profiles[name].Description))
				}
				return app.Out.Print(profiles, strings.Join(lines, "\n"))
			},
		},
	)
	return group
}

func newAuditCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "audit",
		Short:   "Inspect or clear the local destructive-action audit log",
		GroupID: "admin",
	}

	var limit int
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show recent audit log entries",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			entries, err := app.Security.Audit.Read(limit)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return app.Out.Print(entries, "No audit log entries.")
			}
			lines := make([]string, 0, len(entries))
			for _, entry := range entries {
				ts := entry.Timestamp
				if len(ts) >= 19 {
					ts = strings.Replace(ts[:19], "T", " ", 1)
				}
				userSuffix := ""
				if entry.User != "" {
					userSuffix = fmt.Sprintf(" (by %s)", entry.User)
				}
				lines = append(lines, fmt.Sprintf("[%s] %s %s%s", ts, entry.Command, entry.Result, userSuffix))
			}
			return app.Out.Print(entries, strings.Join(lines, "\n"))
		},
	}
	showCmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of entries to display")

	clearCmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the local audit log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			if err := app.Security.Audit.Clear(); err != nil {
				return err
			}
			return app.Out.Print(map[string]any{"cleared": true}, "Audit log cleared.")
		},
	}

	group.AddCommand(showCmd, clearCmd)
	return group
}

func newDoctorCommand(app *App) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Diagnose local setup, voice readiness, and optional live Discord permissions",
		GroupID: "admin",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			report := doctor.Run(doctor.Options{
				Store:     app.Store,
				FlagToken: app.Token,
				Server:    server,
				Logger:    app.Logger,
			})
			if err := app.Out.Print(report, doctor.FormatText(report)); err != nil {
				return err
			}
			if report.Failures > 0 {
				return &output.CLIError{
					Code:     "doctor_failed",
					Message:  fmt.Sprintf("%d diagnostic check(s) failed", report.Failures),
					ExitCode: output.ExitError,
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Also check the bot's live Discord permission bitfield in this server (name or ID)")
	return cmd
}

func newSetupCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "setup",
		Short:   "Interactive setup wizard for token, invite URL, profile, and voice",
		GroupID: "admin",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			return setup.Run(setup.Options{
				Store:       app.Store,
				Security:    app.Security,
				Logger:      app.Logger,
				In:          app.In,
				Out:         app.Stdout,
				Err:         app.Stderr,
				Interactive: app.isInteractive(),
				UseJSON:     app.UseJSON,
			})
		},
	}
}
