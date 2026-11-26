// Package discord wraps disgo's REST and Gateway clients with vox's separation
// of concerns: one-shot commands use HTTP-only REST (no Gateway IDENTIFY),
// while streaming/voice commands construct minimal Gateway intents.
package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	davesession "github.com/thomas-vilte/dave-go/session"

	"github.com/cafecito-games/vox/internal/output"
)

// EventFeatureMap maps CLI event categories to minimal Gateway feature keys.
var EventFeatureMap = map[string]string{
	"messages":  "messages",
	"edits":     "messages",
	"deletes":   "messages",
	"reactions": "reactions",
	"members":   "members",
	"voice":     "voice",
}

// AllEventCategories lists every valid category accepted by --events.
var AllEventCategories = []string{"messages", "reactions", "members", "edits", "deletes", "voice"}

// NewRest constructs an HTTP-only Discord REST client that never opens a
// Gateway websocket or consumes an IDENTIFY.
func NewRest(token string, logger *slog.Logger) rest.Rest {
	client := rest.NewClient(
		token,
		rest.WithLogger(logger),
	)
	return rest.New(client)
}

// ParseEventFilter splits a comma-separated --events option into a normalized set.
// When raw is empty, an empty map is returned to indicate all categories are enabled.
func ParseEventFilter(raw string) (map[string]bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]bool{}, nil
	}
	selected := make(map[string]bool)
	for part := range strings.SplitSeq(trimmed, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, ok := EventFeatureMap[name]; !ok {
			return nil, output.UsageError(fmt.Sprintf(
				"unknown event category %q (valid: %s)",
				name,
				strings.Join(AllEventCategories, ", "),
			))
		}
		selected[name] = true
	}
	return selected, nil
}

// GatewayFeaturesForEvents translates an event filter into the minimal set of
// Gateway feature keys required to receive those events.
func GatewayFeaturesForEvents(eventFilter map[string]bool) []string {
	featureSet := make(map[string]struct{})
	if len(eventFilter) == 0 {
		for _, feature := range EventFeatureMap {
			featureSet[feature] = struct{}{}
		}
	} else {
		for eventName, enabled := range eventFilter {
			if !enabled {
				continue
			}
			if feature, ok := EventFeatureMap[eventName]; ok {
				featureSet[feature] = struct{}{}
			}
		}
	}
	features := make([]string, 0, len(featureSet))
	for feature := range featureSet {
		features = append(features, feature)
	}
	slices.Sort(features)
	return features
}

// BuildGatewayIntents computes the exact minimal Discord Gateway intent bitmask
// for the requested features. It never requests IntentsAll.
func BuildGatewayIntents(features []string) gateway.Intents {
	intents := gateway.IntentGuilds
	for _, feature := range features {
		switch feature {
		case "messages":
			intents = intents.Add(
				gateway.IntentGuildMessages,
				gateway.IntentDirectMessages,
				gateway.IntentMessageContent,
			)
		case "reactions":
			intents = intents.Add(
				gateway.IntentGuildMessages,
				gateway.IntentDirectMessages,
				gateway.IntentGuildMessageReactions,
				gateway.IntentDirectMessageReactions,
			)
		case "members":
			intents = intents.Add(gateway.IntentGuildMembers)
		case "voice":
			intents = intents.Add(gateway.IntentGuildVoiceStates)
		}
	}
	return intents
}

// NewGatewayClient constructs a disgo Gateway client configured with minimal
// intents, structured logging, and pure-Go DAVE E2EE voice support.
func NewGatewayClient(
	token string,
	features []string,
	logger *slog.Logger,
	extraOpts ...bot.ConfigOpt,
) (*bot.Client, error) {
	intents := BuildGatewayIntents(features)

	opts := make([]bot.ConfigOpt, 0, 3+len(extraOpts))
	opts = append(opts,
		bot.WithLogger(logger),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(intents),
			gateway.WithLogger(logger),
		),
		bot.WithVoiceManagerConfigOpts(
			voice.WithLogger(logger),
			voice.WithDaveSessionCreateFunc(davesession.CreateFunc(davesession.WithLogger(logger))),
		),
	)
	opts = append(opts, extraOpts...)

	client, err := disgo.New(token, opts...)
	if err != nil {
		return nil, MapError(err)
	}
	return client, nil
}

// MapError translates disgo REST and Gateway errors into clean, actionable CLI errors.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var cliErr *output.CLIError
	if errors.As(err, &cliErr) {
		return err
	}

	var restErr *rest.Error
	if errors.As(err, &restErr) {
		status := 0
		if restErr.Response != nil {
			status = restErr.Response.StatusCode
		}
		switch status {
		case http.StatusUnauthorized:
			return &output.CLIError{
				Code:     "unauthorized",
				Message:  "Invalid bot token.",
				Hint:     "Verify your token with `vox doctor` or update it via `vox config set token <TOKEN>`.",
				ExitCode: output.ExitError,
				Cause:    err,
			}
		case http.StatusForbidden:
			return &output.CLIError{
				Code:     "discord_forbidden",
				Message:  fmt.Sprintf("Discord API permission denied: %s", restErr.Message),
				Hint:     "Run `vox doctor --server <server>` to check the bot's server permissions and privileged intents.",
				ExitCode: output.ExitError,
				Cause:    err,
			}
		case http.StatusNotFound:
			return &output.CLIError{
				Code:     "discord_not_found",
				Message:  fmt.Sprintf("Discord resource not found: %s", restErr.Message),
				ExitCode: output.ExitError,
				Cause:    err,
			}
		default:
			return &output.CLIError{
				Code:     "discord_api_error",
				Message:  fmt.Sprintf("Discord API error (%d): %s", status, restErr.Message),
				ExitCode: output.ExitError,
				Cause:    err,
			}
		}
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &output.CLIError{
			Code:     "cancelled",
			Message:  err.Error(),
			ExitCode: output.ExitError,
			Cause:    err,
		}
	}

	return &output.CLIError{
		Code:     "discord_error",
		Message:  err.Error(),
		ExitCode: output.ExitError,
		Cause:    err,
	}
}

// IsForbidden reports whether err represents an HTTP 403 Forbidden response from Discord.
func IsForbidden(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusForbidden
}

// FormatUserTag returns a consistent human-readable Discord username representation.
func FormatUserTag(user disgodiscord.User) string {
	if user.Discriminator != "" && user.Discriminator != "0" {
		return user.Username + "#" + user.Discriminator
	}
	return user.Username
}
