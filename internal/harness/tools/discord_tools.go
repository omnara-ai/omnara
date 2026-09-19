package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type discordReadInput struct {
	Resource string `json:"resource,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	Before   string `json:"before,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type discordPostInput struct {
	Resource      string   `json:"resource,omitempty"`
	ThreadID      string   `json:"thread_id,omitempty"`
	Content       string   `json:"content"`
	ArtifactIDs   []string `json:"artifact_ids,omitempty"`
	FollowReplies bool     `json:"follow_replies,omitempty"`
}

func runDiscordTool(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().
		GetToolCall(ctx, call.Turn.ProjectID, call.Turn.AgentID, call.ToolCallID)
	if err != nil {
		return nil, err
	}
	var selector struct {
		Resource string `json:"resource"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(call.Call.Input, &selector); err != nil {
		return appToolFailure(err)
	}
	access, err := call.Executor.resolveAppToolAccess(ctx, call.Turn, record, selector.Resource)
	if err != nil {
		return appToolFailure(err)
	}
	if access.Authority.Original.Scope == nil || access.Authority.Original.Scope.Discord == nil {
		return appToolFailure(errors.New("discord resource requires a conversation scope"))
	}
	address := *access.Authority.Original.Scope.Discord
	if selector.ThreadID != "" {
		address.ThreadID = selector.ThreadID
	}
	scope := appdefinition.Scope{Discord: &address}
	if err := scope.Validate(appdefinition.ProviderDiscord); err != nil || !access.Authority.AllowsScope(scope) {
		return appToolFailure(errors.New("requested Discord conversation is outside the resource scope"))
	}
	client, err := discord.NewClient(discord.Config{
		Credentials: discord.Credentials{
			ApplicationID: access.Connection.ProviderTenantID,
			BotUserID:     access.Connection.ProviderAccountRef,
			BotToken:      access.Credential[secrets.KeyValue],
		},
		HTTPClient: call.Executor.IntegrationHTTPClient,
		BeforeRequest: func(ctx context.Context) error {
			return call.Executor.recheckAppToolAccess(ctx, call.Turn, record, access, scope)
		},
	})
	if err != nil {
		return appToolFailure(err)
	}
	if err := client.CheckIdentity(ctx); err != nil {
		return discordToolFailure(err)
	}
	providerScope := discord.Scope{GuildID: address.GuildID, ChannelID: address.ChannelID, ThreadID: address.ThreadID}
	if record.Name == toolcatalog.ToolNameDiscordRead {
		var input discordReadInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "Discord read"); err != nil {
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
	if err := decodeSingleStrictJSON(call.Call.Input, &input, "Discord post"); err != nil {
		return appToolFailure(err)
	}
	if input.FollowReplies && !access.Authority.AllowsFollowingReplies() {
		return appToolFailure(errors.New("resource does not permit following replies"))
	}
	if input.FollowReplies {
		channel, err := client.GetScopedChannel(ctx, providerScope)
		if err != nil {
			return discordToolFailure(err)
		}
		if channel.GuildID == "" {
			return appToolFailure(
				errors.New("following Discord replies requires a server channel; direct messages are not subscribed"),
			)
		}
		address.GuildID = channel.GuildID
		providerScope.GuildID = channel.GuildID
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
	if input.FollowReplies && address.ThreadID == "" {
		thread, err := client.EnsureThread(ctx, providerScope, message.ID, "Omnara conversation")
		if err != nil {
			content, marshalErr := structuredToolResultContent(
				map[string]any{
					"code":       "thread_creation_failed",
					"message":    "The message was posted, but its reply thread could not be confirmed. Do not resend the message to repair this.",
					"message_id": message.ID,
					"channel_id": message.ChannelID,
				},
			)
			if marshalErr != nil {
				return nil, marshalErr
			}
			return failAsynchronously(content, err), nil
		}
		address.ThreadID = thread.ID
	}
	content, err := structuredToolResultContent(
		map[string]any{
			"resource":       access.Authority.ResourceKey,
			"channel_id":     address.ChannelID,
			"message_id":     message.ID,
			"thread_id":      address.ThreadID,
			"follow_replies": input.FollowReplies,
		},
	)
	if err != nil {
		return nil, err
	}
	return completedAppPost(content, access, scope, input.FollowReplies), nil
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

func discordToolRegistrations() []toolRegistration {
	return []toolRegistration{
		{
			name:            toolcatalog.ToolNameDiscordRead,
			handler:         toolHandler{Async: runDiscordTool},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:            toolcatalog.ToolNameDiscordPostMessage,
			handler:         toolHandler{Async: runDiscordTool},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
	}
}
