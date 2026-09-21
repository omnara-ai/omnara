package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type slackReadInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type slackPostInput struct {
	Text        string   `json:"text,omitempty"`
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

func runSlackTool(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access appToolAccess,
) (asyncPhaseResult, error) {
	address := access.Conversation.Slack
	if address == nil {
		return appToolFailure(errors.New("app conversation does not match Slack"))
	}
	e := call.Executor
	e.IntegrationHTTPClient = slack.WithRequestCheck(e.IntegrationHTTPClient, func(ctx context.Context) error {
		return call.Executor.recheckAppToolAccess(ctx, call.Turn, record, access)
	})
	identity, err := slack.ParseInstallIdentity(access.App.ProviderIdentity)
	if err != nil {
		return appToolFailure(err)
	}
	if err := slack.CheckIdentity(ctx, slack.OAuthConfig{HTTPClient: e.IntegrationHTTPClient},
		access.Credential[secrets.KeyAccessToken], slack.Identity{
			WorkspaceID: access.App.ProviderTenantID, BotUserID: identity.BotUserID,
		}); err != nil {
		return appToolFailure(err)
	}
	target := slack.MessageTarget{
		TargetRef: access.App.Name,
		Channel:   address.ChannelID,
		ThreadTS:  address.ThreadTS,
		BotToken:  access.Credential[secrets.KeyAccessToken],
	}
	if access.Authority.Definition.Operation == toolcatalog.AppOperationRead {
		var input slackReadInput
		if err := decodeSingleStrictJSON(record.Input, &input, "Slack read"); err != nil {
			return appToolFailure(err)
		}
		var page slack.MessagePage
		var result slack.APIResult
		var err error
		var slept time.Duration
		for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
			page, result, err = slack.ReadMessages(ctx, e.IntegrationHTTPClient, target, input.Cursor, input.Limit)
			if err != nil {
				return appToolFailure(err)
			}
			if result == (slack.APIResult{}) {
				content, err := structuredToolResultContent(page)
				return completeAsynchronously(content), err
			}
			if result.RateLimited {
				retry, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &slept, attempt)
				if err != nil {
					return appToolFailure(err)
				}
				if retry {
					continue
				}
			} else if (result.TransientFailure || result.DeliveryUnknown) && attempt < integrationMessageSendAttempts {
				continue // reads are safe to retry
			}
			break
		}
		return slackAppFailure(target.TargetRef, result)
	}
	var input slackPostInput
	if err := decodeSingleStrictJSON(record.Input, &input, "Slack post"); err != nil {
		return appToolFailure(err)
	}
	if len(input.ArtifactIDs) > 0 {
		content, err := e.sendSlackArtifacts(ctx, call.Turn, target, input)
		if err != nil {
			return failAsynchronously(content, err), nil
		}
		return completeAsynchronously(content), nil
	}
	agentID, err := publicid.Encode(publicid.KindAgent, call.Turn.AgentID)
	if err != nil {
		return nil, err
	}
	var result slack.APIResult
	var slept time.Duration
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		result, err = slack.PostMessage(ctx, e.IntegrationHTTPClient, target, agentID, record.ID.String(), input.Text)
		if err != nil {
			return appToolFailure(err)
		}
		if result.MessageID != "" {
			break
		}
		if result.RateLimited {
			retry, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &slept, attempt)
			if err != nil {
				return appToolFailure(err)
			}
			if retry {
				continue
			}
		}
		if result.DeliveryUnknown || result.TransientFailure {
			messageID, found, _, readErr := slack.ReconcileMessage(
				ctx,
				e.IntegrationHTTPClient,
				target,
				agentID,
				record.ID.String(),
				record.CreatedAt,
			)
			if readErr == nil && found {
				result.MessageID = messageID
				break
			}
			// Absence from a paginated read is not proof that publication failed.
			result = slack.APIResult{
				DeliveryUnknown: true,
				Code:            "delivery_unknown",
				Message:         "Slack may have accepted the message. Read the conversation before deciding whether to resend.",
			}
		}
		return slackAppFailure(target.TargetRef, result)
	}
	channel, timestamp, found := strings.Cut(result.MessageID, ":")
	if !found || channel != address.ChannelID {
		return appToolFailure(errors.New("slack publication was not confirmed for this conversation"))
	}
	content, err := structuredToolResultContent(
		map[string]any{
			"app":        access.App.Name,
			"channel_id": channel,
			"message_ts": timestamp,
			"thread_ts":  address.ThreadTS,
		},
	)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func slackAppFailure(resource string, result slack.APIResult) (asyncPhaseResult, error) {
	content, err := slackAppFailureContent(resource, result)
	return failAsynchronously(content, err), nil
}

func slackAppFailureContent(resource string, result slack.APIResult) (toolResultContent, error) {
	code := result.Code
	if result.RateLimited {
		code = "rate_limited"
	}
	if result.DeliveryUnknown {
		code = "delivery_unknown"
	}
	if code == "" {
		code = "provider_error"
	}
	content, err := structuredToolResultContent(
		map[string]any{
			"app":                 resource,
			"code":                code,
			"message":             result.Message,
			"retry_after_seconds": result.RetryAfter.Seconds(),
		},
	)
	if err != nil {
		return toolResultContent{}, err
	}
	message := result.Message
	if message == "" {
		message = code
	}
	return content, errors.New(message)
}
