package tools

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type discordReadInput struct {
	Before string `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type discordPostInput struct {
	Content     string   `json:"content"`
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

func runDiscordTool(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access appToolAccess,
) (asyncPhaseResult, error) {
	address := access.Conversation.Discord
	if address == nil {
		return appToolFailure(errors.New("app conversation does not match Discord"))
	}
	client, err := discord.NewClient(discord.Config{
		Credentials: discord.Credentials{
			ApplicationID: access.App.ProviderTenantID,
			BotUserID:     access.App.ProviderAccountRef,
			BotToken:      access.Credential[secrets.KeyValue],
		},
		HTTPClient: call.Executor.IntegrationHTTPClient,
		BeforeRequest: func(ctx context.Context) error {
			return call.Executor.recheckAppToolAccess(ctx, call.Turn, record, access)
		},
	})
	if err != nil {
		return appToolFailure(err)
	}
	if err := client.CheckIdentity(ctx); err != nil {
		return discordToolFailure(err)
	}
	providerScope := discord.Scope{ChannelID: address.ChannelID, ThreadID: address.ThreadID}
	if access.Authority.Definition.Operation == toolcatalog.AppOperationRead {
		var input discordReadInput
		if err := decodeSingleStrictJSON(record.Input, &input, "Discord read"); err != nil {
			return appToolFailure(err)
		}
		page, err := client.ListMessages(
			ctx,
			providerScope,
			discord.PageOptions{Before: input.Before, Limit: input.Limit},
		)
		if err != nil {
			return discordToolFailure(err)
		}
		content, err := structuredToolResultContent(page)
		return completeAsynchronously(content), err
	}
	var input discordPostInput
	if err := decodeSingleStrictJSON(record.Input, &input, "Discord post"); err != nil {
		return appToolFailure(err)
	}
	args := discord.MessageArgs{Content: input.Content, Nonce: base64.RawURLEncoding.EncodeToString(record.ID[:])}
	var total int
	for _, encoded := range input.ArtifactIDs {
		id, err := publicid.Decode(publicid.KindArtifact, encoded)
		if err != nil {
			return appToolFailure(err)
		}
		content, artifact, err := call.Executor.Store.Artifacts().
			GetArtifactBlob(ctx, call.Turn.ProjectID, call.Turn.AgentID, id)
		if err != nil {
			return appToolFailure(err)
		}
		total += len(content)
		if len(content) > discord.MaxFileBytes || total > discord.MaxUploadBytes {
			return appToolFailure(errors.New("discord uploads exceed the allowed size"))
		}
		args.Files = append(
			args.Files,
			discord.Upload{
				Filename: modelcontext.MediaFilename(artifact.Filename, artifact.ContentType),
				Content:  content,
			},
		)
	}
	message, err := client.CreateMessage(ctx, providerScope, args)
	if err != nil {
		return discordToolFailure(err)
	}
	content, err := structuredToolResultContent(
		map[string]any{
			"app":        access.App.Name,
			"channel_id": address.ChannelID,
			"message_id": message.ID,
			"thread_id":  address.ThreadID,
		},
	)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func discordToolFailure(err error) (asyncPhaseResult, error) {
	var apiErr *discord.APIError
	if !errors.As(err, &apiErr) {
		return appToolFailure(err)
	}
	content, marshalErr := structuredToolResultContent(
		map[string]any{
			"code":                apiErr.Code,
			"message":             apiErr.Error(),
			"retry_after_seconds": apiErr.RetryAfter.Seconds(),
		},
	)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return failAsynchronously(content, err), nil
}
