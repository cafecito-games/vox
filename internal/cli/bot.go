package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

func newBotCommand(app *App) *cobra.Command {
	group := &cobra.Command{
		Use:     "bot",
		Short:   "Inspect and configure a Discord bot and its application",
		GroupID: "admin",
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show the active bot's profile, application, install settings, and intents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.WithRest(cmd, func(client rest.Rest) error {
				ctx := rest.WithCtx(cmd.Context())
				application, err := client.GetCurrentApplication(ctx)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				user, err := client.GetCurrentUser("", ctx)
				if err != nil {
					return voxdiscord.MapError(err)
				}
				return app.Out.Print(map[string]any{
					"application": applicationDetails(application),
					"profile":     botProfileDetails(user.User),
				}, strings.Join([]string{
					fmt.Sprintf("Bot %s (%s)", user.Username, user.ID),
					"Avatar: " + botSettingText(user.AvatarURL()),
					"Banner: " + botSettingText(user.BannerURL()),
					applicationSummary(application),
				}, "\n"))
			})
		},
	}
	profileCmd := &cobra.Command{Use: "profile", Short: "Manage the bot user's profile"}
	profileCmd.AddCommand(newBotProfileEditCommand(app))
	applicationCmd := &cobra.Command{Use: "application", Short: "Manage the bot's Discord application"}
	applicationCmd.AddCommand(newBotApplicationEditCommand(app))
	group.AddCommand(showCmd, profileCmd, applicationCmd, newBotInviteCommand(app), newBotProvisionCommand(app))
	return group
}

func newBotInviteCommand(app *App) *cobra.Command {
	var applicationIDArg, guildIDArg string
	var permissions, bundles []string
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Generate a Discord server install URL with explicit permissions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			var guildID snowflake.ID
			if cmd.Flags().Changed("guild-id") {
				var valid bool
				guildID, valid = voxdiscord.ParseSnowflake(guildIDArg)
				if !valid {
					return output.UsageError("--guild-id must be a Discord server ID")
				}
			}
			names := slices.Clone(permissions)
			for _, bundleName := range bundles {
				index := slices.IndexFunc(voxdiscord.InviteBundles, func(bundle voxdiscord.InviteBundle) bool {
					return bundle.Name == bundleName
				})
				if index < 0 {
					return output.UsageError(fmt.Sprintf("unknown permission bundle %q", bundleName))
				}
				names = append(names, voxdiscord.InviteBundles[index].Permissions...)
			}
			mask, err := voxdiscord.ParsePermissionNames(names)
			if err != nil {
				return err
			}
			printInvite := func(applicationID snowflake.ID) error {
				inviteURL := voxdiscord.BuildGuildOAuthInviteURL(applicationID, mask, guildID)
				payload := map[string]any{
					"application_id":   applicationID,
					"permissions":      mask,
					"permission_names": voxdiscord.PermissionNames(mask),
					"url":              inviteURL,
				}
				if guildID != 0 {
					payload["guild_id"] = guildID
				}
				return app.Out.Print(payload, inviteURL)
			}
			if cmd.Flags().Changed("application-id") {
				applicationID, valid := voxdiscord.ParseSnowflake(applicationIDArg)
				if !valid {
					return output.UsageError("--application-id must be a Discord application ID")
				}
				return printInvite(applicationID)
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				application, err := client.GetCurrentApplication(rest.WithCtx(cmd.Context()))
				if err != nil {
					return voxdiscord.MapError(err)
				}
				if application.BotRequireCodeGrant {
					return &output.CLIError{
						Code:     "oauth2_code_grant_required",
						Message:  "This application requires the full Discord OAuth2 authorization code flow.",
						Hint:     "Use the application's configured OAuth2 installation flow.",
						ExitCode: output.ExitError,
					}
				}
				return printInvite(application.ID)
			})
		},
	}
	cmd.Flags().StringVar(&applicationIDArg, "application-id", "", "Generate the URL offline for this application ID")
	cmd.Flags().StringVar(&guildIDArg, "guild-id", "", "Lock the Discord authorization screen to this server ID")
	cmd.Flags().StringSliceVar(&permissions, "permissions", nil, "Discord permission names to request (comma-separated)")
	cmd.Flags().StringSliceVar(&bundles, "bundle", nil, "Permission bundles to include (messaging, moderation, voice, admin)")
	return cmd
}

func newBotProfileEditCommand(app *App) *cobra.Command {
	var username, avatar, banner string
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit the bot's username, avatar, or banner",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			var update disgodiscord.UserUpdate
			fields := make([]string, 0, 3)
			if cmd.Flags().Changed("username") {
				if strings.TrimSpace(username) == "" {
					return output.UsageError("--username must not be blank")
				}
				update.Username = username
				fields = append(fields, "username")
			}
			for _, image := range []struct {
				name string
				path string
				dest *omit.Omit[*disgodiscord.Icon]
			}{{"avatar", avatar, &update.Avatar}, {"banner", banner, &update.Banner}} {
				if !cmd.Flags().Changed(image.name) {
					continue
				}
				icon, err := botImage(image.path)
				if err != nil {
					return err
				}
				*image.dest = icon
				fields = append(fields, image.name)
			}
			if len(fields) == 0 {
				return output.UsageError("specify at least one profile setting to edit")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				user, err := client.UpdateCurrentUser(update, rest.WithCtx(cmd.Context()))
				if err != nil {
					return voxdiscord.MapError(err)
				}
				_ = app.Security.Audit.Record("bot profile edit", map[string]any{
					"bot_id": user.ID, "fields": fields,
				}, "ok", "")
				return app.Out.Print(botProfileDetails(user.User), fmt.Sprintf("Updated bot profile for %s (%s).", user.Username, user.ID))
			})
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "Bot username")
	cmd.Flags().StringVar(&avatar, "avatar", "", "Avatar image file (pass an empty value to remove it)")
	cmd.Flags().StringVar(&banner, "banner", "", "Banner image file (pass an empty value to remove it)")
	return cmd
}

type botIntentSetting struct {
	flag    string
	limited disgodiscord.ApplicationFlags
	full    disgodiscord.ApplicationFlags
}

var botIntentSettings = []botIntentSetting{
	{"message-content", disgodiscord.ApplicationFlagGatewayMessageContentLimited, disgodiscord.ApplicationFlagGatewayMessageContent},
	{"server-members", disgodiscord.ApplicationFlagGatewayGuildMemberLimited, disgodiscord.ApplicationFlagGatewayGuildMembers},
	{"presence", disgodiscord.ApplicationFlagGatewayPresenceLimited, disgodiscord.ApplicationFlagGatewayPresence},
}

func newBotApplicationEditCommand(app *App) *cobra.Command {
	var description, icon, cover, installURL, interactionsURL string
	var tags, permissions []string
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit app metadata, server install defaults, or limited Gateway intents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			patch := make(map[string]any)
			for _, setting := range []struct{ flag, key, value string }{
				{"description", "description", description},
				{"custom-install-url", "custom_install_url", installURL},
				{"interactions-endpoint-url", "interactions_endpoint_url", interactionsURL},
			} {
				if cmd.Flags().Changed(setting.flag) {
					patch[setting.key] = setting.value
				}
			}
			if cmd.Flags().Changed("tags") {
				patch["tags"] = append([]string{}, tags...)
			}
			for _, image := range []struct{ flag, key, path string }{
				{"icon", "icon", icon},
				{"cover-image", "cover_image", cover},
			} {
				if cmd.Flags().Changed(image.flag) {
					imageValue, err := botImage(image.path)
					if err != nil {
						return err
					}
					patch[image.key] = imageValue.Value
				}
			}
			permissionMask, err := voxdiscord.ParsePermissionNames(permissions)
			if err != nil {
				return err
			}
			var installPermissions *disgodiscord.Permissions
			if cmd.Flags().Changed("guild-install-permissions") {
				installPermissions = &permissionMask
			}
			intents := make(map[string]bool)
			for _, intent := range botIntentSettings {
				if cmd.Flags().Changed(intent.flag) {
					enabled, err := cmd.Flags().GetBool(intent.flag)
					if err != nil {
						return err
					}
					intents[intent.flag] = enabled
				}
			}
			if len(patch) == 0 && installPermissions == nil && len(intents) == 0 {
				return output.UsageError("specify at least one application setting to edit")
			}
			return app.WithRest(cmd, func(client rest.Rest) error {
				ctx := rest.WithCtx(cmd.Context())
				var application *disgodiscord.Application
				if installPermissions != nil || len(intents) != 0 {
					application, err = client.GetCurrentApplication(ctx)
					if err != nil {
						return voxdiscord.MapError(err)
					}
					if err := addBotApplicationDefaults(application, patch, installPermissions, intents); err != nil {
						return err
					}
				}
				if len(patch) == 0 {
					return app.Out.Print(applicationDetails(application), "Discord application already matches the requested settings.")
				}
				updated, err := patchBotApplication(client, patch, ctx)
				if err != nil {
					return err
				}
				_ = app.Security.Audit.Record("bot application edit", map[string]any{
					"application_id": updated.ID, "fields": slices.Sorted(maps.Keys(patch)),
				}, "ok", "")
				return app.Out.Print(applicationDetails(updated), applicationSummary(updated))
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Application description")
	cmd.Flags().StringVar(&icon, "icon", "", "Application icon file (pass an empty value to remove it)")
	cmd.Flags().StringVar(&cover, "cover-image", "", "Application cover image file (pass an empty value to remove it)")
	cmd.Flags().StringVar(&installURL, "custom-install-url", "", "Authorization URL for an app configured to use a custom install link")
	cmd.Flags().StringVar(&interactionsURL, "interactions-endpoint-url", "", "Discord-validated HTTP endpoint for interactions")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "Application tags (comma-separated, empty clears them)")
	cmd.Flags().StringSliceVar(&permissions, "guild-install-permissions", nil, "Replace the Discord permission names requested on server install (empty requests zero)")
	for _, intent := range botIntentSettings {
		cmd.Flags().Bool(intent.flag, false, "Set the limited "+intent.flag+" Gateway intent (use =false to disable)")
	}
	return cmd
}

func addBotApplicationDefaults(application *disgodiscord.Application, patch map[string]any,
	permissions *disgodiscord.Permissions, intents map[string]bool,
) error {
	if permissions != nil {
		key, value, err := voxdiscord.GuildInstallPermissionUpdate(application, *permissions)
		if err != nil {
			return err
		}
		if key != "" {
			patch[key] = value
		}
	}
	flags := application.Flags
	for _, intent := range botIntentSettings {
		enabled, specified := intents[intent.flag]
		if !specified {
			continue
		}
		if flags.Has(intent.full) {
			if !enabled {
				return output.UsageError(fmt.Sprintf("disable the approved %s intent in the Discord Developer Portal", intent.flag))
			}
			continue
		}
		flags = flags.Remove(intent.limited)
		if enabled {
			flags = flags.Add(intent.limited)
		}
	}
	if flags != application.Flags {
		patch["flags"] = flags
	}
	return nil
}

func patchBotApplication(client rest.Rest, patch map[string]any, opts ...rest.RequestOpt) (*disgodiscord.Application, error) {
	// A map preserves deliberately empty strings and lists and explicit null images.
	var updated disgodiscord.Application
	if err := client.Do(rest.UpdateCurrentApplication.Compile(nil), patch, &updated, opts...); err != nil {
		return nil, voxdiscord.MapError(err)
	}
	return &updated, nil
}

func botImage(path string) (omit.Omit[*disgodiscord.Icon], error) {
	if path == "" {
		return omit.NewNilPtr[disgodiscord.Icon](), nil
	}
	icon, err := loadDiscordIcon(path)
	if err != nil {
		return omit.Omit[*disgodiscord.Icon]{}, err
	}
	return omit.New(icon), nil
}

func botProfileDetails(user disgodiscord.User) map[string]any {
	return map[string]any{
		"id": user.ID, "username": user.Username, "avatar_url": user.AvatarURL(), "banner_url": user.BannerURL(),
	}
}

func applicationDetails(application *disgodiscord.Application) map[string]any {
	intents := make(map[string]string, len(botIntentSettings))
	for _, intent := range botIntentSettings {
		intents[intent.flag] = intent.status(application.Flags)
	}
	return map[string]any{
		"id": application.ID, "name": application.Name, "description": application.Description, "tags": application.Tags,
		"icon_url": application.IconURL(), "cover_image_url": application.CoverImageURL(),
		"bot_public": application.BotPublic, "bot_require_code_grant": application.BotRequireCodeGrant,
		"custom_install_url": application.CustomInstallURL, "interactions_endpoint_url": application.InteractionsEndpointURL,
		"install_params": application.InstallParams, "integration_types_config": application.IntegrationTypesConfig,
		"privileged_intents": intents,
	}
}

func applicationSummary(application *disgodiscord.Application) string {
	lines := []string{
		fmt.Sprintf("Application %s (%s)", application.Name, application.ID),
		"Description: " + application.Description,
		"Tags: " + noneOrJoin(application.Tags),
		"Icon: " + botSettingText(application.IconURL()),
		"Cover image: " + botSettingText(application.CoverImageURL()),
		"Custom install URL: " + botSettingText(application.CustomInstallURL),
		"Interactions endpoint URL: " + botSettingText(application.InteractionsEndpointURL),
		fmt.Sprintf("Public bot: %t", application.BotPublic),
		fmt.Sprintf("Require OAuth2 code grant: %t", application.BotRequireCodeGrant),
	}
	if len(application.IntegrationTypesConfig) == 0 {
		lines = append(lines, installParamsText("Server install defaults", application.InstallParams))
	}
	for kind, label := range []string{"Server install defaults", "User install defaults"} {
		if config, ok := application.IntegrationTypesConfig[disgodiscord.ApplicationIntegrationType(kind)]; ok {
			lines = append(lines, installParamsText(label, config.OAuth2InstallParams))
		}
	}
	for _, intent := range botIntentSettings {
		lines = append(lines, fmt.Sprintf("Intent %s: %s", intent.flag, intent.status(application.Flags)))
	}
	return strings.Join(lines, "\n")
}

func installParamsText(label string, params *disgodiscord.InstallParams) string {
	if params == nil {
		return label + ": none"
	}
	return fmt.Sprintf("%s: scopes %v, permissions %s", label, params.Scopes, noneOrJoin(voxdiscord.PermissionNames(params.Permissions)))
}

func botSettingText(value *string) string {
	if value == nil {
		return "none"
	}
	return *value
}

func (intent botIntentSetting) status(flags disgodiscord.ApplicationFlags) string {
	switch {
	case flags.Has(intent.full):
		return "approved"
	case flags.Has(intent.limited):
		return "limited"
	default:
		return "disabled"
	}
}
