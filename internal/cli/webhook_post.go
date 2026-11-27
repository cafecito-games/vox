package cli

import (
	"bytes"
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

const (
	webhookURLEnv         = "DISCORD_WEBHOOK_URL"
	webhookOutputID       = "id"
	webhookOutputGitHub   = "github-output"
	webhookUnconfiguredID = "webhook_unconfigured"
)

type preparedWebhookPayload struct {
	body       any
	components bool
}

type webhookPostedMessage struct {
	ID        snowflake.ID `json:"id"`
	ChannelID snowflake.ID `json:"channel_id"`
}

func newWebhookPostCommand(app *App) *cobra.Command {
	var embed voxdiscord.EmbedSpec
	var fieldsJSON, payloadPath, thread, outputMode string
	var uploads []string
	var bestEffort, truncateEmbeds, escapeFields bool
	cmd := &cobra.Command{
		Use:   "post [text]",
		Short: "Post to Discord using DISCORD_WEBHOOK_URL",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Enforce(cmd); err != nil {
				return err
			}
			if outputMode != "" {
				if app.UseJSON {
					return output.UsageError("--output cannot be combined with --json")
				}
				if outputMode != webhookOutputID && outputMode != webhookOutputGitHub {
					return output.UsageError("--output must be one of: id, github-output")
				}
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

			embedOptions := voxdiscord.EmbedBuildOptions{
				Truncate:     truncateEmbeds,
				EscapeFields: escapeFields,
			}
			payloads, isBatch, err := webhookPostPayloads(cmd.InOrStdin(), payloadPath, args, embed, len(files) > 0, embedOptions)
			if err != nil {
				return err
			}
			if len(files) > 0 && len(payloads) > 1 {
				return output.UsageError("--file cannot be combined with multi-payload --payload-file")
			}

			rawURL := strings.TrimSpace(os.Getenv(webhookURLEnv))
			if rawURL == "" && bestEffort {
				app.Out.Note("warning: %s is not configured; skipping Discord notification.", webhookURLEnv)
				return renderWebhookPostResult(app.Out, outputMode, bestEffort, isBatch, nil, len(payloads), webhookUnconfiguredID)
			}
			target, err := voxdiscord.ParseWebhookTarget(rawURL)
			if err != nil {
				return err
			}
			if err := target.ValidateThread(threadID); err != nil {
				return err
			}

			var client rest.Client
			if app.newRest != nil {
				client = app.newRest("", slog.New(slog.DiscardHandler))
			} else {
				client = voxdiscord.NewWebhookRest()
			}
			defer client.Close(context.Background())

			delivered := make([]webhookPostedMessage, 0, len(payloads))
			for _, item := range payloads {
				body := item.body
				if len(files) > 0 {
					body, err = disgodiscord.PayloadWithFiles(item.body, files...)
					if err != nil {
						return fmt.Errorf("build webhook attachment upload: %w", err)
					}
				}
				reqCtx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				message, postErr := target.Post(reqCtx, client, body, threadID, item.components)
				cancel()
				if postErr != nil {
					exitCode := output.ExitCodeFor(postErr)
					if exitCode == output.ExitInterrupted || exitCode == output.ExitUsage {
						return postErr
					}
					reason := webhookErrorCode(postErr)
					if bestEffort {
						if !isBatch && len(payloads) == 1 {
							app.Out.Note("warning: %s", postErr.Error())
						} else {
							app.Out.Note("warning: Delivered %d of %d Discord webhook messages before failure: %s",
								len(delivered), len(payloads), postErr.Error())
						}
						return renderWebhookPostResult(app.Out, outputMode, bestEffort, isBatch, delivered, len(payloads), reason)
					}
					if !isBatch && len(payloads) == 1 {
						return postErr
					}
					return &output.CLIError{
						Code: reason,
						Message: fmt.Sprintf("Delivered %d of %d Discord webhook messages before failure: %s",
							len(delivered), len(payloads), postErr.Error()),
						Details: map[string]any{
							"sent":     len(delivered),
							"total":    len(payloads),
							"messages": delivered,
						},
						ExitCode: output.ExitError,
						Cause:    postErr,
					}
				}
				delivered = append(delivered, webhookPostedMessage{
					ID:        message.ID,
					ChannelID: message.ChannelID,
				})
			}

			return renderWebhookPostResult(app.Out, outputMode, bestEffort, isBatch, delivered, len(payloads), "")
		},
	}
	cmd.Flags().StringVar(&payloadPath, "payload-file", "", "Discord webhook JSON payload file: single object, JSON array, or JSONL (use - for stdin)")
	cmd.Flags().StringVar(&thread, "thread", "", "Thread ID to post into")
	cmd.Flags().StringArrayVar(&uploads, "file", nil, "File path to attach (repeatable)")
	cmd.Flags().StringVar(&embed.Title, "embed-title", "", "Embed title")
	cmd.Flags().StringVar(&embed.Description, "embed-desc", "", "Embed description")
	cmd.Flags().StringVar(&embed.URL, "embed-url", "", "Link for the embed title")
	cmd.Flags().StringVar(&embed.Color, "embed-color", "", "Embed color as a hex RGB value")
	cmd.Flags().StringVar(&fieldsJSON, "embed-fields-json", "", "JSON array of embed fields (name, value, inline)")
	cmd.Flags().BoolVar(&bestEffort, "best-effort", false, "Log delivery errors or an unset DISCORD_WEBHOOK_URL to stderr and exit 0")
	cmd.Flags().StringVar(&outputMode, "output", "", "Output format override (id, github-output)")
	cmd.Flags().BoolVar(&truncateEmbeds, "truncate", false, "Truncate overlong embed text and counts to Discord's limits with an ellipsis")
	cmd.Flags().BoolVar(&escapeFields, "escape-fields", false, "Escape Discord markdown and link syntax in embed field names and values")
	return cmd
}

func webhookPostPayloads(
	stdin io.Reader,
	path string,
	args []string,
	embed voxdiscord.EmbedSpec,
	hasFiles bool,
	embedOptions voxdiscord.EmbedBuildOptions,
) ([]preparedWebhookPayload, bool, error) {
	const suppressedMentions = `{"parse":[]}`
	if path != "" {
		if len(args) != 0 || !embed.IsEmpty() {
			return nil, false, output.UsageError("--payload-file cannot be combined with text or embed options")
		}
		reader := stdin
		if path != "-" {
			file, err := os.Open(path)
			if err != nil {
				return nil, false, fmt.Errorf("open webhook payload file: %w", err)
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		rawObjects, isBatch, err := decodeWebhookPayloadObjects(reader)
		if err != nil {
			return nil, false, err
		}
		prepared := make([]preparedWebhookPayload, 0, len(rawObjects))
		for index, payload := range rawObjects {
			if len(payload["allowed_mentions"]) == 0 || strings.TrimSpace(string(payload["allowed_mentions"])) == "null" {
				payload["allowed_mentions"] = json.RawMessage(suppressedMentions)
			}
			if err := voxdiscord.PrepareRawPayloadEmbeds(payload, embedOptions); err != nil {
				if isBatch {
					return nil, false, output.UsageError(fmt.Sprintf("payload %d: %s", index+1, err.Error()))
				}
				return nil, false, err
			}
			prepared = append(prepared, preparedWebhookPayload{
				body:       payload,
				components: len(payload["components"]) > 0 && strings.TrimSpace(string(payload["components"])) != "null",
			})
		}
		return prepared, isBatch, nil
	}

	payload := map[string]any{"allowed_mentions": json.RawMessage(suppressedMentions)}
	if len(args) > 0 && args[0] != "" {
		payload["content"] = args[0]
	}
	if !embed.IsEmpty() {
		built, err := voxdiscord.BuildEmbedWithOptions(embed, embedOptions)
		if err != nil {
			return nil, false, err
		}
		payload["embeds"] = []disgodiscord.Embed{built}
	}
	if len(payload) == 1 && !hasFiles {
		return nil, false, output.UsageError("provide text, an embed, a JSON payload, or a file to post")
	}
	return []preparedWebhookPayload{{body: payload}}, false, nil
}

func decodeWebhookPayloadObjects(reader io.Reader) ([]map[string]json.RawMessage, bool, error) {
	const usageMessage = "--payload-file must contain one Discord webhook JSON object, a JSON array of objects, or JSONL objects"
	rawBytes, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, fmt.Errorf("read webhook payload file: %w", err)
	}
	trimmed := bytes.TrimSpace(rawBytes)
	if len(trimmed) == 0 {
		return nil, false, output.UsageError(usageMessage)
	}

	if trimmed[0] == '[' {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		var objects []map[string]json.RawMessage
		var extra json.RawMessage
		if err := decoder.Decode(&objects); err != nil || len(objects) == 0 || !errors.Is(decoder.Decode(&extra), io.EOF) {
			return nil, false, output.UsageError(usageMessage)
		}
		for _, object := range objects {
			if object == nil {
				return nil, false, output.UsageError(usageMessage)
			}
		}
		return objects, true, nil
	}

	if trimmed[0] != '{' {
		return nil, false, output.UsageError(usageMessage)
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var objects []map[string]json.RawMessage
	for {
		var object map[string]json.RawMessage
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || object == nil {
			return nil, false, output.UsageError(usageMessage)
		}
		objects = append(objects, object)
	}
	if len(objects) == 0 {
		return nil, false, output.UsageError(usageMessage)
	}
	return objects, len(objects) > 1, nil
}

func renderWebhookPostResult(
	writer *output.Writer,
	outputMode string,
	bestEffort bool,
	isBatch bool,
	delivered []webhookPostedMessage,
	total int,
	failureReason string,
) error {
	if delivered == nil {
		delivered = []webhookPostedMessage{}
	}
	allDelivered := failureReason == "" && len(delivered) == total && total > 0

	switch outputMode {
	case webhookOutputID:
		var builder strings.Builder
		for _, message := range delivered {
			builder.WriteString(message.ID.String())
			builder.WriteByte('\n')
		}
		if builder.Len() == 0 {
			return nil
		}
		return writer.PrintRaw(builder.String())

	case webhookOutputGitHub:
		messageID := ""
		if allDelivered && len(delivered) == 1 && total == 1 {
			messageID = delivered[0].ID.String()
		}
		ids := make([]string, len(delivered))
		for index, message := range delivered {
			ids[index] = message.ID.String()
		}
		encodedIDs, err := json.Marshal(ids)
		if err != nil {
			return fmt.Errorf("encode message IDs: %w", err)
		}
		var builder strings.Builder
		fmt.Fprintf(&builder, "message-id=%s\n", messageID)
		fmt.Fprintf(&builder, "message-ids=%s\n", encodedIDs)
		fmt.Fprintf(&builder, "delivered=%t\n", allDelivered)
		return writer.PrintRaw(builder.String())
	}

	if !isBatch {
		if !allDelivered {
			plain := fmt.Sprintf("Discord webhook delivery failed (%s); continuing because --best-effort is set.", failureReason)
			if failureReason == webhookUnconfiguredID {
				plain = fmt.Sprintf("Skipped Discord notification (%s is not configured).", webhookURLEnv)
			}
			return writer.Print(map[string]any{
				"id":        nil,
				"delivered": false,
				"reason":    failureReason,
			}, plain)
		}
		message := delivered[0]
		data := map[string]any{
			"id":         message.ID,
			"channel_id": message.ChannelID,
		}
		if bestEffort {
			data["delivered"] = true
		}
		return writer.Print(data, fmt.Sprintf("Posted Discord message %s in channel %s.", message.ID, message.ChannelID))
	}

	if !allDelivered {
		plain := fmt.Sprintf("Delivered %d of %d Discord webhook messages (%s); continuing because --best-effort is set.",
			len(delivered), total, failureReason)
		if failureReason == webhookUnconfiguredID {
			plain = fmt.Sprintf("Skipped %d Discord notifications (%s is not configured).", total, webhookURLEnv)
		}
		return writer.Print(map[string]any{
			"delivered": false,
			"sent":      len(delivered),
			"total":     total,
			"reason":    failureReason,
			"messages":  delivered,
		}, plain)
	}

	plain := fmt.Sprintf("Posted %d Discord messages.", len(delivered))
	if bestEffort {
		return writer.Print(map[string]any{
			"delivered": true,
			"sent":      len(delivered),
			"total":     total,
			"messages":  delivered,
		}, plain)
	}
	return writer.Print(delivered, plain)
}

func webhookErrorCode(err error) string {
	var cliErr *output.CLIError
	if errors.As(err, &cliErr) && cliErr.Code != "" {
		return cliErr.Code
	}
	return "webhook_post_failed"
}
