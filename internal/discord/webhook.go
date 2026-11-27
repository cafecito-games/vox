package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/cafecito-games/vox/internal/output"
)

var webhookPath = regexp.MustCompile(`^/api/(?:v[0-9]+/)?webhooks/([0-9]+)/([A-Za-z0-9_-]+)/?$`)

// WebhookTarget contains the credential and thread for a Discord incoming webhook.
type WebhookTarget struct {
	id         snowflake.ID
	token      string
	threadID   snowflake.ID
	components bool
}

// ParseWebhookTarget reads a Discord webhook URL without including credentials in errors.
func ParseWebhookTarget(raw string) (WebhookTarget, error) {
	invalid := output.UsageError("DISCORD_WEBHOOK_URL must be an HTTPS Discord incoming webhook URL")
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return WebhookTarget{}, invalid
	}
	switch strings.ToLower(u.Host) {
	case "discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com":
	default:
		return WebhookTarget{}, invalid
	}
	parts := webhookPath.FindStringSubmatch(u.EscapedPath())
	if len(parts) != 3 {
		return WebhookTarget{}, invalid
	}
	id, ok := ParseSnowflake(parts[1])
	if !ok {
		return WebhookTarget{}, invalid
	}
	target := WebhookTarget{id: id, token: parts[2]}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return WebhookTarget{}, invalid
	}
	for key, values := range query {
		if len(values) != 1 {
			return WebhookTarget{}, invalid
		}
		switch key {
		case "thread_id":
			target.threadID, ok = ParseSnowflake(values[0])
			if !ok {
				return WebhookTarget{}, invalid
			}
		case "wait", "with_components":
			if values[0] != "true" && values[0] != "false" {
				return WebhookTarget{}, invalid
			}
			if key == "with_components" {
				target.components = values[0] == "true"
			}
		default:
			return WebhookTarget{}, invalid
		}
	}
	return target, nil
}

// NewWebhookRest creates a bot-free Discord client with loggers disabled because
// the webhook credential is part of the request path.
func NewWebhookRest() rest.Client {
	logger := slog.New(slog.DiscardHandler)
	return rest.NewClient("",
		rest.WithLogger(logger),
		rest.WithRateLimiterConfigOpts(rest.WithRateLimiterLogger(logger)),
		rest.WithHTTPClient(&http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}),
	)
}

// ValidateThread verifies that an explicit thread ID does not conflict with
// a thread_id query parameter in DISCORD_WEBHOOK_URL.
func (target WebhookTarget) ValidateThread(threadID snowflake.ID) error {
	if threadID != 0 && target.threadID != 0 && target.threadID != threadID {
		return output.UsageError("--thread conflicts with thread_id in DISCORD_WEBHOOK_URL")
	}
	return nil
}

// Post sends a payload to Discord and waits for a confirmed message.
func (target WebhookTarget) Post(ctx context.Context, client rest.Client, body any, threadID snowflake.ID, components bool) (*disgodiscord.Message, error) {
	if err := target.ValidateThread(threadID); err != nil {
		return nil, err
	}
	if threadID == 0 {
		threadID = target.threadID
	}
	query := (rest.CreateWebhookMessageParams{
		Wait: true, ThreadID: threadID, WithComponents: components || target.components,
	}).ToQueryValues()
	var msg disgodiscord.Message
	if err := client.Do(rest.CreateWebhookMessage.Compile(query, target.id, target.token), body, &msg, rest.WithCtx(ctx)); err != nil {
		// Discord and transport errors can include the URL, request data, or echoed secrets.
		var restErr *rest.Error
		var netErr net.Error
		switch {
		case errors.As(err, &restErr) && restErr.Response != nil:
			return nil, output.NewError("webhook_post_failed", fmt.Sprintf("Discord webhook delivery failed (HTTP %d, Discord code %d).",
				restErr.Response.StatusCode, restErr.Code))
		case errors.Is(err, context.Canceled):
			return nil, output.InterruptedError("Webhook request cancelled.")
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
			return nil, output.NewError("webhook_timeout", "Discord did not confirm the webhook message before the timeout.")
		default:
			return nil, output.NewError("webhook_post_failed", "Discord did not confirm the webhook message.")
		}
	}
	if msg.ID == 0 || msg.ChannelID == 0 {
		return nil, output.NewError("webhook_post_unconfirmed", "Discord did not return a confirmed webhook message and channel ID.")
	}
	return &msg, nil
}
