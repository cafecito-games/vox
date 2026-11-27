package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/spf13/cobra"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

const webhookURLEnv = "DISCORD_WEBHOOK_URL"

func newWebhookPostCommand(app *App) *cobra.Command {
	var embed voxdiscord.EmbedSpec
	var fieldsJSON, payloadPath, thread string
	var uploads []string
	cmd := &cobra.Command{
		Use:   "post [text]",
		Short: "Post to Discord using DISCORD_WEBHOOK_URL",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			target, err := voxdiscord.ParseWebhookTarget(os.Getenv(webhookURLEnv))
			if err != nil {
				return err
			}
			var threadID snowflake.ID
			if cmd.Flags().Changed("thread") {
				var valid bool
				threadID, valid = voxdiscord.ParseSnowflake(thread)
				if !valid {
					return output.UsageError("--thread must be a Discord thread ID")
				}
			}
			if fieldsJSON != "" {
				if err := json.Unmarshal([]byte(fieldsJSON), &embed.Fields); err != nil || embed.Fields == nil {
					return output.UsageError("--embed-fields-json must contain a JSON array of embed fields")
				}
			}
			files, cleanup, err := voxdiscord.OpenFiles(uploads)
			if err != nil {
				return err
			}
			defer cleanup()
			for _, file := range files {
				if strings.ContainsAny(file.Name, "\"\\\r\n") {
					return output.UsageError("attachment file names must be valid multipart filenames")
				}
			}
			payload, components, err := webhookPostPayload(cmd.InOrStdin(), payloadPath, args, embed, len(files) > 0)
			if err != nil {
				return err
			}
			body := payload
			if len(files) > 0 {
				body, err = disgodiscord.PayloadWithFiles(payload, files...)
				if err != nil {
					return fmt.Errorf("build webhook attachment upload: %w", err)
				}
			}
			var client rest.Client
			if app.newRest != nil {
				client = app.newRest("", slog.New(slog.DiscardHandler))
			} else {
				client = voxdiscord.NewWebhookRest()
			}
			defer client.Close(context.Background())
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			msg, err := target.Post(ctx, client, body, threadID, components)
			if err != nil {
				return err
			}
			return app.Out.Print(map[string]any{"id": msg.ID, "channel_id": msg.ChannelID},
				fmt.Sprintf("Posted Discord message %s in channel %s.", msg.ID, msg.ChannelID))
		},
	}
	cmd.Flags().StringVar(&payloadPath, "payload-file", "", "Discord webhook JSON payload file (use - for stdin)")
	cmd.Flags().StringVar(&thread, "thread", "", "Thread ID to post into")
	cmd.Flags().StringArrayVar(&uploads, "file", nil, "File path to attach (repeatable)")
	cmd.Flags().StringVar(&embed.Title, "embed-title", "", "Embed title")
	cmd.Flags().StringVar(&embed.Description, "embed-desc", "", "Embed description")
	cmd.Flags().StringVar(&embed.URL, "embed-url", "", "Link for the embed title")
	cmd.Flags().StringVar(&embed.Color, "embed-color", "", "Embed color as a hex RGB value")
	cmd.Flags().StringVar(&fieldsJSON, "embed-fields-json", "", "JSON array of embed fields (name, value, inline)")
	return cmd
}

func webhookPostPayload(stdin io.Reader, path string, args []string, embed voxdiscord.EmbedSpec, hasFiles bool) (any, bool, error) {
	const suppressedMentions = `{"parse":[]}`
	if path != "" {
		if len(args) != 0 || !embed.IsEmpty() {
			return nil, false, output.UsageError("--payload-file cannot be combined with text or embed options")
		}
		if path != "-" {
			file, err := os.Open(path)
			if err != nil {
				return nil, false, fmt.Errorf("open webhook payload file: %w", err)
			}
			defer func() { _ = file.Close() }()
			stdin = file
		}
		decoder := json.NewDecoder(stdin)
		var payload map[string]json.RawMessage
		var extra json.RawMessage
		if err := decoder.Decode(&payload); err != nil || payload == nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
			return nil, false, output.UsageError("--payload-file must contain one Discord webhook JSON object")
		}
		if len(payload["allowed_mentions"]) == 0 || string(payload["allowed_mentions"]) == "null" {
			payload["allowed_mentions"] = json.RawMessage(suppressedMentions)
		}
		return payload, len(payload["components"]) > 0, nil
	}
	payload := map[string]any{"allowed_mentions": json.RawMessage(suppressedMentions)}
	if len(args) > 0 && args[0] != "" {
		payload["content"] = args[0]
	}
	if !embed.IsEmpty() {
		built, err := voxdiscord.BuildEmbed(embed)
		if err != nil {
			return nil, false, err
		}
		payload["embeds"] = []disgodiscord.Embed{built}
	}
	if len(payload) == 1 && !hasFiles {
		return nil, false, output.UsageError("provide text, an embed, a JSON payload, or a file to post")
	}
	return payload, false, nil
}
