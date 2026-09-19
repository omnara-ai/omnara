package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type githubReadInput struct {
	Resource string `json:"resource,omitempty"`
	Section  string `json:"section,omitempty"`
	Page     int    `json:"page,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type githubDiscussionInput struct {
	Resource string `json:"resource,omitempty"`
	Body     string `json:"body"`
}

type githubInlineInput struct {
	Resource string `json:"resource,omitempty"`
	github.InlineCommentArgs
}

type githubReplyInput struct {
	Resource  string `json:"resource,omitempty"`
	CommentID int64  `json:"comment_id"`
	Body      string `json:"body"`
}

func runGitHubTool(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().
		GetToolCall(ctx, call.Turn.ProjectID, call.Turn.AgentID, call.ToolCallID)
	if err != nil {
		return nil, err
	}
	var selector struct {
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(call.Call.Input, &selector); err != nil {
		return nil, err
	}
	access, err := call.Executor.resolveAppToolAccess(ctx, call.Turn, record, selector.Resource)
	if err != nil {
		return appToolFailure(err)
	}
	if access.Authority.Original.Scope == nil || access.Authority.Original.Scope.GitHub == nil ||
		!access.Authority.AllowsScope(*access.Authority.Original.Scope) {
		return appToolFailure(errors.New("GitHub PR scope is no longer available"))
	}
	client, err := call.Executor.githubToolClient(call.Turn, record, access)
	if err != nil {
		return appToolFailure(err)
	}
	address := access.Authority.Original.Scope.GitHub
	scope := github.Scope{RepositoryID: address.RepositoryID, PullRequest: address.PullRequest}
	var result any
	switch record.Name {
	case toolcatalog.ToolNameGitHubRead:
		var input githubReadInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "GitHub read"); err != nil {
			return appToolFailure(err)
		}
		options := github.PageOptions{Page: input.Page, PerPage: input.Limit}
		switch input.Section {
		case "", "pull_request":
			result, err = client.GetPullRequest(ctx, scope)
		case "discussion_comments":
			result, err = client.ListDiscussionComments(ctx, scope, options)
		case "review_comments":
			result, err = client.ListReviewComments(ctx, scope, options)
		case "files":
			result, err = client.ListFiles(ctx, scope, options)
		case "diff":
			result, err = client.GetDiff(ctx, scope)
		default:
			return appToolFailure(errors.New("unknown GitHub read section"))
		}
	case toolcatalog.ToolNameGitHubDiscussionComment:
		var input githubDiscussionInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "GitHub discussion comment"); err != nil {
			return appToolFailure(err)
		}
		result, err = client.CreateDiscussionComment(ctx, scope, input.Body)
	case toolcatalog.ToolNameGitHubInlineComment:
		var input githubInlineInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "GitHub inline comment"); err != nil {
			return appToolFailure(err)
		}
		result, err = client.CreateInlineComment(ctx, scope, input.InlineCommentArgs)
	case toolcatalog.ToolNameGitHubReply:
		var input githubReplyInput
		if err := decodeSingleStrictJSON(call.Call.Input, &input, "GitHub review reply"); err != nil {
			return appToolFailure(err)
		}
		result, err = client.Reply(ctx, scope, input.CommentID, input.Body)
	default:
		return nil, fmt.Errorf("unsupported GitHub tool %q", record.Name)
	}
	if err != nil {
		return appToolFailure(err)
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
	access appToolAccess,
) (*github.Client, error) {
	appID, err := strconv.ParseInt(access.Credential[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 || access.Connection.ProviderTenantID != strconv.FormatInt(appID, 10) {
		return nil, errors.New("GitHub app credentials do not match the connection")
	}
	installationID, err := strconv.ParseInt(access.Connection.ProviderAccountRef, 10, 64)
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
			return e.recheckAppToolAccess(ctx, turn, tool, access, *access.Authority.Original.Scope)
		},
	})
}

func appToolFailure(err error) (asyncPhaseResult, error) {
	result := struct {
		Code              string `json:"code"`
		Message           string `json:"message"`
		RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	}{Code: "app_tool_failed", Message: err.Error()}
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

func githubToolRegistrations() []toolRegistration {
	var registrations []toolRegistration
	for _, name := range []string{
		toolcatalog.ToolNameGitHubRead, toolcatalog.ToolNameGitHubDiscussionComment,
		toolcatalog.ToolNameGitHubInlineComment, toolcatalog.ToolNameGitHubReply,
	} {
		registrations = append(
			registrations,
			toolRegistration{
				name:            name,
				handler:         toolHandler{Async: runGitHubTool},
				permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
			},
		)
	}
	return registrations
}
