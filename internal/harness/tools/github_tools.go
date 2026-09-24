package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type githubReadInput struct {
	Section string `json:"section,omitempty"`
	Page    int    `json:"page,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type githubDiscussionInput struct {
	Body string `json:"body"`
}

type githubInlineInput struct {
	github.InlineCommentArgs
}

type githubReplyInput struct {
	CommentID int64  `json:"comment_id"`
	Body      string `json:"body"`
}

func runGitHubTool(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access integrationToolAccess,
) (asyncPhaseResult, error) {
	address := access.Conversation.GitHub
	if address == nil {
		return integrationToolFailure(errors.New("integration conversation does not match GitHub"))
	}
	client, err := call.Executor.githubToolClient(call.Turn, record, access)
	if err != nil {
		return integrationToolFailure(err)
	}
	providerScope := github.Scope{RepositoryID: address.RepositoryID, PullRequest: address.PullRequest}
	var result any
	switch access.Authority.Definition.Operation {
	case toolcatalog.IntegrationOperationRead:
		var input githubReadInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub read"); err != nil {
			return integrationToolFailure(err)
		}
		options := github.PageOptions{Page: input.Page, PerPage: input.Limit}
		switch input.Section {
		case "", "pull_request":
			result, err = client.GetPullRequest(ctx, providerScope)
		case "discussion_comments":
			result, err = client.ListDiscussionComments(ctx, providerScope, options)
		case "review_comments":
			result, err = client.ListReviewComments(ctx, providerScope, options)
		case "files":
			result, err = client.ListFiles(ctx, providerScope, options)
		case "diff":
			result, err = client.GetDiff(ctx, providerScope)
		default:
			return integrationToolFailure(errors.New("unknown GitHub read section"))
		}
	case toolcatalog.IntegrationOperationDiscussionComment:
		var input githubDiscussionInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub discussion comment"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.CreateDiscussionComment(ctx, providerScope, input.Body)
	case toolcatalog.IntegrationOperationInlineComment:
		var input githubInlineInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub inline comment"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.CreateInlineComment(ctx, providerScope, input.InlineCommentArgs)
	case toolcatalog.IntegrationOperationReply:
		var input githubReplyInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub review reply"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.Reply(ctx, providerScope, input.CommentID, input.Body)
	default:
		return nil, fmt.Errorf("unsupported GitHub tool %q", record.Name)
	}
	if err != nil {
		return integrationToolFailure(err)
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func (e Executor) githubToolClient(
	turn Turn,
	tool executionstore.ToolCallRecord,
	access integrationToolAccess,
) (*github.Client, error) {
	appID, err := strconv.ParseInt(access.Credential[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 || access.Integration.ProviderTenantID != strconv.FormatInt(appID, 10) {
		return nil, errors.New("GitHub App credentials do not match the integration")
	}
	installationID, err := strconv.ParseInt(access.Integration.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 {
		return nil, errors.New("GitHub installation identity is invalid")
	}
	return github.NewClient(github.Config{
		Credentials: github.Credentials{
			AppID:         appID,
			PrivateKeyPEM: access.Credential[secrets.KeyPrivateKey],
			WebhookSecret: access.Credential[secrets.KeyWebhookSecret],
		},
		InstallationID: installationID,
		HTTPClient:     e.IntegrationHTTPClient,
		BeforeRequest: func(ctx context.Context) error {
			return e.recheckIntegrationToolAccess(ctx, turn, tool, access)
		},
	})
}

func integrationToolFailure(err error) (asyncPhaseResult, error) {
	result := struct {
		Code              string `json:"code"`
		Message           string `json:"message"`
		RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	}{Code: "integration_tool_failed", Message: err.Error()}
	var apiError *github.APIError
	if errors.As(err, &apiError) {
		result.Code = string(apiError.Code)
		result.RetryAfterSeconds = int64(apiError.RetryAfter.Seconds())
		if apiError.Code == github.DeliveryUnknown {
			result.Message = "GitHub may have accepted this message. Read the PR before deciding whether to resend."
		}
	}
	content, marshalErr := structuredToolResultContent(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return failAsynchronously(content, err), nil
}
