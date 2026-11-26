package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cafecito-games/vox/internal/buildinfo"
	"github.com/cafecito-games/vox/internal/output"
	"github.com/cafecito-games/vox/internal/security"
	"github.com/cafecito-games/vox/internal/serve"
)

// FlagSchema describes one CLI flag in `vox schema --json`.
type FlagSchema struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
}

// CommandSchema describes one CLI command node in `vox schema --json`.
type CommandSchema struct {
	Path        string          `json:"path"`
	Use         string          `json:"use"`
	Short       string          `json:"short"`
	Destructive bool            `json:"destructive,omitempty"`
	Flags       []FlagSchema    `json:"flags,omitempty"`
	Subcommands []CommandSchema `json:"subcommands,omitempty"`
}

// ToolSchema is the top-level document emitted by `vox schema --json` for AI agents.
type ToolSchema struct {
	Name         string                      `json:"name"`
	Version      string                      `json:"version"`
	ExitCodes    map[string]int              `json:"exit_codes"`
	GlobalFlags  []FlagSchema                `json:"global_flags"`
	Profiles     map[string]security.Profile `json:"profiles"`
	Commands     []CommandSchema             `json:"commands"`
	ServeActions []serve.ActionSpec          `json:"serve_actions"`
}

func newSchemaCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "schema",
		Short:   "Emit machine-readable CLI & serve protocol schema for AI agents",
		Long:    "Outputs a complete JSON description of every vox command, flag, exit code, permission profile, and `vox serve` JSONL action so AI agents can bootstrap without scraping help text.",
		GroupID: "admin",
		RunE: func(_ *cobra.Command, _ []string) error {
			schema := BuildToolSchema(app.Root)
			if app.UseJSON {
				return app.Out.Print(schema, "")
			}
			var lines []string
			lines = append(lines, fmt.Sprintf("vox %s — %d top-level command groups, %d serve JSONL actions",
				schema.Version, len(schema.Commands), len(schema.ServeActions)))
			lines = append(lines, "Run `vox schema --json` for the complete machine-readable schema.")
			lines = append(lines, "")
			for _, cmd := range schema.Commands {
				lines = append(lines, fmt.Sprintf("  %-14s %s", cmd.Path, cmd.Short))
			}
			return app.Out.Print(schema, strings.Join(lines, "\n"))
		},
	}
}

// BuildToolSchema introspects the live Cobra command tree and serve action catalog.
func BuildToolSchema(root *cobra.Command) ToolSchema {
	globalFlags := extractFlags(root.PersistentFlags())
	commands := make([]CommandSchema, 0, len(root.Commands()))
	for _, sub := range root.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		commands = append(commands, buildCommandSchema(sub))
	}
	slices.SortFunc(commands, func(a, b CommandSchema) int {
		return strings.Compare(a.Path, b.Path)
	})

	return ToolSchema{
		Name:    "vox",
		Version: buildinfo.Current().Version,
		ExitCodes: map[string]int{
			"ok":                output.ExitOK,
			"error":             output.ExitError,
			"usage_error":       output.ExitUsage,
			"permission_denied": output.ExitPermissionDenied,
		},
		GlobalFlags:  globalFlags,
		Profiles:     security.DefaultProfiles(),
		Commands:     commands,
		ServeActions: serve.AllActions,
	}
}

func buildCommandSchema(cmd *cobra.Command) CommandSchema {
	path := security.NormalizeCommandPath(cmd.CommandPath())
	schema := CommandSchema{
		Path:        path,
		Use:         cmd.Use,
		Short:       cmd.Short,
		Destructive: security.IsDestructive(path),
		Flags:       extractFlags(cmd.NonInheritedFlags()),
	}
	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		schema.Subcommands = append(schema.Subcommands, buildCommandSchema(sub))
	}
	return schema
}

func extractFlags(flagSet *pflag.FlagSet) []FlagSchema {
	var out []FlagSchema
	flagSet.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Name == "help" {
			return
		}
		out = append(out, FlagSchema{
			Name:      flag.Name,
			Shorthand: flag.Shorthand,
			Type:      flag.Value.Type(),
			Default:   flag.DefValue,
			Usage:     flag.Usage,
		})
	})
	return out
}
