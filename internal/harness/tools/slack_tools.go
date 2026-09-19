package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type slackReadInput struct {
	Resource string `json:"resource,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type slackPostInput struct {
	Resource      string   `json:"resource,omitempty"`
	ThreadTS      string   `json:"thread_ts,omitempty"`
	Text          string   `json:"text,omitempty"`
	ArtifactIDs   []string `json:"artifact_ids,omitempty"`
	FollowReplies bool     `json:"follow_replies,omitempty"`
}

func runSlackTool(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().
		GetToolCall(ctx, call.Turn.ProjectID, call.Turn.AgentID, call.ToolCallID)
	if err != nil {
		return nil, err
	}
	var selector struct {
		Resource string `json:"resource"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal(call.Call.Input, &selector); err != nil {
		return appToolFailure(err)
	}
	access, err := call.Executor.resolveAppToolAccess(ctx, call.Turn, record, selector.Resource)
	if err != nil {
		return appToolFailure(err)
	}
	if access.Authority.Original.Scope == nil || access.Authority.Original.Scope.Slack == nil {
		return appToolFailure(errors.New("slack resource requires a conversation scope"))
	}
	address := *access.Authority.Original.Scope.Slack
	if selector.ThreadTS != "" {
		address.ThreadTS = selector.ThreadTS
	}
	scope := appdefinition.Scope{Slack: &address}
	if err := scope.Validate(appdefinition.ProviderSlack); err != nil || !access.Authority.AllowsScope(scope) {
		return appToolFailure(errors.New("requested Slack conversation is outside the resource scope"))
	}
	e := call.Executor
	e.IntegrationHTTPClient = slack.WithRequestCheck(e.IntegrationHTTPClient, func(ctx context.Context) error {
		return call.Executor.recheckAppToolAccess(ctx, call.Turn, record, access, scope)
	})
	target := slack.MessageTarget{
		TargetRef: access.Authority.ResourceKey,
		Channel:   address.ChannelID,
		ThreadTS:  address.ThreadTS,
		BotToken:  access.Credential[secrets.KeyAccessToken],
	}
	if record.Name == toolcatalog.ToolNameSlackRead {
		var input slackReadInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "Slack read"); err != nil {
			return appToolFailure(err)
		}
		var page slack.MessagePage
		var result slack.APIResult
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
	if err := decodeSingleStrictJSON(call.Call.Input, &input, "Slack post"); err != nil {
		return appToolFailure(err)
	}
	if input.FollowReplies && !access.Authority.AllowsFollowingReplies() {
		return appToolFailure(errors.New("resource does not permit following replies"))
	}
	followScope := scope
	if strings.HasPrefix(address.ChannelID, "D") {
		// Slack DM inputs address the whole direct conversation, even when a
		// specific outgoing message is threaded. Never subscribe to a thread
		// address that the incoming provider never produces.
		followScope = appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: address.ChannelID}}
	}
	if input.FollowReplies && !access.Authority.AllowsScope(followScope) {
		return appToolFailure(errors.New("resource does not permit following this direct conversation"))
	}
	if input.FollowReplies && len(input.ArtifactIDs) > 0 && address.ThreadTS == "" &&
		!strings.HasPrefix(address.ChannelID, "D") {
		return appToolFailure(
			errors.New(
				"to follow a new Slack conversation with files, first post its text with follow_replies, then attach the files to the returned thread_ts",
			),
		)
	}
	if len(input.ArtifactIDs) > 0 {
		content, err := e.sendSlackArtifacts(ctx, call.Turn, target, input)
		if err != nil {
			return failAsynchronously(content, err), nil
		}
		return completedAppPost(content, access, followScope, input.FollowReplies), nil
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
	if address.ThreadTS == "" && !strings.HasPrefix(address.ChannelID, "D") {
		address.ThreadTS = timestamp
	}
	if err := scope.Validate(appdefinition.ProviderSlack); err != nil {
		return appToolFailure(errors.New("slack returned an invalid message timestamp"))
	}
	content, err := structuredToolResultContent(
		map[string]any{
			"resource":       access.Authority.ResourceKey,
			"channel_id":     channel,
			"message_ts":     timestamp,
			"thread_ts":      address.ThreadTS,
			"follow_replies": input.FollowReplies,
		},
	)
	if err != nil {
		return nil, err
	}
	return completedAppPost(content, access, followScope, input.FollowReplies), nil
}

func completedAppPost(
	content toolResultContent,
	access appToolAccess,
	scope appdefinition.Scope,
	follow bool,
) asyncPhaseResult {
	if !follow {
		return completeAsynchronously(content)
	}
	return completeAsync{
		content: content,
		follow: &executionstore.ConfirmedAppFollow{
			ResourceKey:  access.Authority.ResourceKey,
			ConnectionID: access.Connection.ID,
			Scope:        scope,
		},
	}
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
			"resource":            resource,
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

func slackToolRegistrations() []toolRegistration {
	return []toolRegistration{
		{
			name:            toolcatalog.ToolNameSlackRead,
			handler:         toolHandler{Async: runSlackTool},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:            toolcatalog.ToolNameSlackPostMessage,
			handler:         toolHandler{Async: runSlackTool},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
	}
}
