package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/cafecito-games/vox/internal/config"
	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

const discordApplicationsURL = "https://discord.com/developers/applications"

var provisionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type botProvisionSpec struct {
	Version       int                     `yaml:"version"`
	Name          string                  `yaml:"name"`
	GuildID       string                  `yaml:"guild_id"`
	ApplicationID string                  `yaml:"application_id"`
	Permissions   []string                `yaml:"permissions"`
	Profile       botProvisionProfile     `yaml:"profile"`
	Application   botProvisionApplication `yaml:"application"`
}

type botProvisionProfile struct {
	Username *string `yaml:"username"`
}

type botProvisionApplication struct {
	Description             *string   `yaml:"description"`
	Tags                    *[]string `yaml:"tags"`
	CustomInstallURL        *string   `yaml:"custom_install_url"`
	InteractionsEndpointURL *string   `yaml:"interactions_endpoint_url"`
	GuildInstallPermissions *[]string `yaml:"guild_install_permissions"`
	Intents                 struct {
		MessageContent *bool `yaml:"message_content"`
		ServerMembers  *bool `yaml:"server_members"`
		Presence       *bool `yaml:"presence"`
	} `yaml:"intents"`
}

type botProvisionResult struct {
	Name                string                    `json:"name"`
	Status              string                    `json:"status"`
	GuildID             snowflake.ID              `json:"guild_id"`
	ApplicationID       snowflake.ID              `json:"application_id,omitempty"`
	RequiredPermissions []string                  `json:"required_permissions"`
	LivePermissions     *disgodiscord.Permissions `json:"live_permissions,omitempty"`
	MissingPermissions  []string                  `json:"missing_permissions,omitempty"`
	PortalURL           string                    `json:"portal_url,omitempty"`
	InstallURL          string                    `json:"install_url,omitempty"`
	NextAction          string                    `json:"next_action,omitempty"`
}

func newBotProvisionCommand(app *App) *cobra.Command {
	var applicationIDArg string
	cmd := &cobra.Command{
		Use:   "provision <bot.yaml>",
		Short: "Apply a bot setup spec, with resumable Discord creation and install steps",
		Long:  "Apply a YAML or JSON bot setup spec. Run again after each Discord handoff until the status is ready. Use --json to read the status and next action programmatically.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			spec, err := loadBotProvisionSpec(args[0])
			if err != nil {
				return err
			}
			guildID, valid := voxdiscord.ParseSnowflake(spec.GuildID)
			if !valid {
				return output.UsageError("guild_id must be a Discord server ID")
			}
			applicationID, err := provisionApplicationID(spec.ApplicationID, applicationIDArg, cmd.Flags().Changed("application-id"))
			if err != nil {
				return err
			}
			permissions, err := voxdiscord.ParsePermissionNames(spec.Permissions)
			if err != nil {
				return err
			}
			var installPermissions *disgodiscord.Permissions
			if spec.Application.GuildInstallPermissions != nil {
				mask, err := voxdiscord.ParsePermissionNames(*spec.Application.GuildInstallPermissions)
				if err != nil {
					return err
				}
				installPermissions = &mask
			}

			stateDir := filepath.Join(app.Store.BaseDir, "bot-provisions", "bot-"+spec.Name)
			if err := config.EnsureSecureDir(stateDir); err != nil {
				return err
			}
			if err := claimProvisionID(filepath.Join(stateDir, "guild_id"), guildID); err != nil {
				return err
			}
			bindingPath := filepath.Join(stateDir, "application_id")
			savedID, err := readProvisionID(bindingPath)
			if err != nil {
				return err
			}
			if savedID != 0 {
				if applicationID != 0 && applicationID != savedID {
					return provisionBindingError(bindingPath, savedID)
				}
				applicationID = savedID
			}
			result := botProvisionResult{
				Name: spec.Name, GuildID: guildID, ApplicationID: applicationID,
				RequiredPermissions: voxdiscord.PermissionNames(permissions),
			}
			switch applicationID {
			case 0:
				result.Status = "needs_discord_app"
				result.PortalURL = discordApplicationsURL
				result.NextAction = "Create or select an app in Discord, then rerun with --application-id or set application_id in the spec."
			default:
				token, _, err := app.Store.ResolveToken(app.Token)
				if errors.Is(err, config.ErrNoToken) {
					result.Status = "needs_bot_token"
					result.PortalURL = discordApplicationsURL + "/" + applicationID.String() + "/bot"
					result.NextAction = "Get the app's token from Discord and rerun with DISCORD_BOT_TOKEN supplied by your secret store."
					break
				}
				if err != nil {
					return err
				}
				err = app.withTokenRest(token, func(client rest.Rest) error {
					return app.provisionBot(client, rest.WithCtx(cmd.Context()), spec, &result, bindingPath, permissions, installPermissions)
				})
				if err != nil {
					return err
				}
			}
			return app.Out.Print(result, result.text())
		},
	}
	cmd.Flags().StringVar(&applicationIDArg, "application-id", "", "Discord application ID to bind after validating the bot token")
	return cmd
}

func loadBotProvisionSpec(path string) (botProvisionSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return botProvisionSpec{}, fmt.Errorf("read bot provision spec: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var spec botProvisionSpec
	if err := decoder.Decode(&spec); err != nil {
		return spec, output.UsageError(fmt.Sprintf("parse bot provision spec: %v", err))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return spec, output.UsageError("the bot provision spec must contain exactly one YAML or JSON document")
	}
	switch {
	case spec.Version != 0 && spec.Version != 1:
		return spec, output.UsageError("bot provision spec version must be 1")
	case !provisionNamePattern.MatchString(spec.Name):
		return spec, output.UsageError("name must be 1 to 63 lowercase letters, digits, or hyphens and start with a letter or digit")
	case spec.Profile.Username != nil && strings.TrimSpace(*spec.Profile.Username) == "":
		return spec, output.UsageError("profile.username must not be blank")
	default:
		return spec, nil
	}
}

func provisionApplicationID(specID, flagID string, flagSet bool) (snowflake.ID, error) {
	var id snowflake.ID
	if specID != "" {
		var valid bool
		id, valid = voxdiscord.ParseSnowflake(specID)
		if !valid {
			return 0, output.UsageError("application_id must be a Discord application ID")
		}
	}
	if flagSet {
		value, valid := voxdiscord.ParseSnowflake(flagID)
		if !valid {
			return 0, output.UsageError("--application-id must be a Discord application ID")
		}
		if id != 0 && id != value {
			return 0, output.UsageError("--application-id differs from application_id in the spec")
		}
		id = value
	}
	return id, nil
}

func (app *App) provisionBot(client rest.Rest, ctx rest.RequestOpt, spec botProvisionSpec, result *botProvisionResult,
	bindingPath string, permissions disgodiscord.Permissions, installPermissions *disgodiscord.Permissions,
) error {
	application, err := client.GetCurrentApplication(ctx)
	if err != nil {
		return voxdiscord.MapError(err)
	}
	if application == nil || application.ID != result.ApplicationID {
		return output.NewError("application_mismatch", fmt.Sprintf("The bot token does not belong to Discord application %s.", result.ApplicationID))
	}
	if err := claimProvisionID(bindingPath, application.ID); err != nil {
		return err
	}
	patch, err := spec.Application.patch(application, installPermissions)
	if err != nil {
		return err
	}
	user, err := client.GetCurrentUser("", ctx)
	if err != nil {
		return voxdiscord.MapError(err)
	}
	if user == nil || user.ID == 0 {
		return output.NewError("invalid_discord_response", "Discord did not return the authenticated bot user.")
	}
	if len(patch) != 0 {
		application, err = patchBotApplication(client, patch, ctx)
		if err != nil {
			return err
		}
		_ = app.Security.Audit.Record("bot provision", map[string]any{
			"application_id": result.ApplicationID, "phase": "application", "fields": slices.Sorted(maps.Keys(patch)),
		}, "ok", "")
	}
	if username := spec.Profile.Username; username != nil && user.Username != *username {
		if _, err := client.UpdateCurrentUser(disgodiscord.UserUpdate{Username: *username}, ctx); err != nil {
			return voxdiscord.MapError(err)
		}
		_ = app.Security.Audit.Record("bot provision", map[string]any{
			"application_id": result.ApplicationID, "phase": "profile", "fields": []string{"username"},
		}, "ok", "")
	}
	return result.checkInstallation(client, ctx, application, user.ID, permissions)
}

func (settings botProvisionApplication) patch(application *disgodiscord.Application, permissions *disgodiscord.Permissions) (map[string]any, error) {
	patch := make(map[string]any)
	for _, field := range []struct {
		key     string
		desired *string
		current string
	}{
		{"description", settings.Description, application.Description},
		{"custom_install_url", settings.CustomInstallURL, valueOrEmpty(application.CustomInstallURL)},
		{"interactions_endpoint_url", settings.InteractionsEndpointURL, valueOrEmpty(application.InteractionsEndpointURL)},
	} {
		if field.desired != nil && *field.desired != field.current {
			patch[field.key] = *field.desired
		}
	}
	if settings.Tags != nil && !slices.Equal(*settings.Tags, application.Tags) {
		patch["tags"] = append([]string{}, (*settings.Tags)...)
	}
	intents := make(map[string]bool)
	for name, enabled := range map[string]*bool{
		"message-content": settings.Intents.MessageContent,
		"server-members":  settings.Intents.ServerMembers,
		"presence":        settings.Intents.Presence,
	} {
		if enabled != nil {
			intents[name] = *enabled
		}
	}
	if err := addBotApplicationDefaults(application, patch, permissions, intents); err != nil {
		return nil, err
	}
	return patch, nil
}

func (result *botProvisionResult) checkInstallation(client rest.Rest, ctx rest.RequestOpt, application *disgodiscord.Application,
	userID snowflake.ID, permissions disgodiscord.Permissions,
) error {
	guilds, err := voxdiscord.FetchAllGuilds(client, ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(guilds, func(guild disgodiscord.OAuth2Guild) bool { return guild.ID == result.GuildID }) {
		result.Status = "needs_install_approval"
		result.NextAction = "Have an authorized server member open the install link in Discord, then rerun this command."
		if application.BotRequireCodeGrant {
			result.InstallURL = valueOrEmpty(application.CustomInstallURL)
			if result.InstallURL == "" {
				result.PortalURL = discordApplicationsURL + "/" + application.ID.String() + "/installation"
				result.NextAction = "Use the application's Discord OAuth2 authorization code flow to install it, then rerun this command."
			}
		} else {
			result.InstallURL = voxdiscord.BuildGuildOAuthInviteURL(application.ID, permissions, result.GuildID)
		}
		return nil
	}
	live, err := client.GetGuildMemberPermissions(result.GuildID, userID, ctx)
	if err != nil {
		return voxdiscord.MapError(err)
	}
	result.LivePermissions = &live
	if !live.Has(disgodiscord.PermissionAdministrator) && !live.Has(permissions) {
		result.Status = "needs_permissions"
		result.MissingPermissions = voxdiscord.PermissionNames(permissions.Remove(live))
		result.NextAction = "Grant the missing server permissions to the bot, then rerun this command."
		return nil
	}
	result.Status = "ready"
	return nil
}

func (result botProvisionResult) text() string {
	lines := []string{fmt.Sprintf("%s (%s)", result.Name, result.Status)}
	for _, line := range []string{result.NextAction, result.PortalURL, result.InstallURL} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(result.MissingPermissions) != 0 {
		lines = append(lines, "Missing permissions: "+strings.Join(result.MissingPermissions, ", "))
	}
	return strings.Join(lines, "\n")
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
