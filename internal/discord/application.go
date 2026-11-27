package discord

import (
	"maps"
	"slices"

	disgodiscord "github.com/disgoorg/disgo/discord"

	"github.com/cafecito-games/vox/internal/output"
)

// GuildInstallPermissionUpdate prepares a patch for the current app's guild
// install defaults. Older apps expose their settings in install_params.
func GuildInstallPermissionUpdate(application *disgodiscord.Application, permissions disgodiscord.Permissions) (string, any, error) {
	contexts := application.IntegrationTypesConfig
	guild, hasGuild := contexts[disgodiscord.ApplicationIntegrationTypeGuildInstall]
	if !hasGuild && len(contexts) != 0 {
		return "", nil, output.UsageError("enable Guild Install for this application in the Discord Developer Portal")
	}
	current := application.InstallParams
	if hasGuild {
		current = guild.OAuth2InstallParams
	}
	if current != nil && current.Permissions == permissions && slices.Contains(current.Scopes, disgodiscord.OAuth2ScopeBot) {
		return "", nil, nil
	}
	params := &disgodiscord.InstallParams{
		Scopes:      []disgodiscord.OAuth2Scope{disgodiscord.OAuth2ScopeBot, disgodiscord.OAuth2ScopeApplicationsCommands},
		Permissions: permissions,
	}
	if current != nil && len(current.Scopes) != 0 {
		params.Scopes = slices.Clone(current.Scopes)
		if !slices.Contains(params.Scopes, disgodiscord.OAuth2ScopeBot) {
			params.Scopes = append(params.Scopes, disgodiscord.OAuth2ScopeBot)
		}
	}
	if !hasGuild {
		return "install_params", params, nil
	}
	next := maps.Clone(contexts)
	guild.OAuth2InstallParams = params
	next[disgodiscord.ApplicationIntegrationTypeGuildInstall] = guild
	return "integration_types_config", next, nil
}
