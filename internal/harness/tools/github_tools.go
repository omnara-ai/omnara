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
	Section  string `json:"section,omitempty"`
	Page     int    `json:"page,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	ReviewID int64  `json:"review_id,omitempty"`
}

type githubDiscussionInput struct {
	Body string `json:"body"`
}

type githubReviewCommentInput struct {
	github.ReviewCommentArgs
	ReviewID int64 `json:"review_id,omitempty"`
}

type githubStartReviewInput struct {
	CommitID string `json:"commit_id"`
}

type githubSubmitReviewInput struct {
	ReviewID int64  `json:"review_id"`
	Body     string `json:"body"`
}

type githubDiscardReviewInput struct {
	ReviewID int64 `json:"review_id"`
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
		if input.Cursor != "" && input.Section != "review_threads" {
			return integrationToolFailure(errors.New("cursor is only supported for GitHub review_threads"))
		}
		if input.Section == "review_threads" && input.Page != 0 {
			return integrationToolFailure(errors.New("GitHub review_threads uses cursor, not page"))
		}
		if input.ReviewID != 0 && input.Section != "review" && input.Section != "review_comments" {
			return integrationToolFailure(errors.New("review_id is only supported for GitHub review and review_comments"))
		}
		options := github.PageOptions{Page: input.Page, PerPage: input.Limit}
		switch input.Section {
		case "", "pull_request":
			result, err = client.GetPullRequest(ctx, providerScope)
		case "discussion_comments":
			result, err = client.ListDiscussionComments(ctx, providerScope, options)
		case "review_comments":
			if input.ReviewID != 0 {
				result, err = client.ListReviewCommentsForReview(ctx, providerScope, input.ReviewID, options)
			} else {
				result, err = client.ListReviewComments(ctx, providerScope, options)
			}
		case "reviews":
			result, err = client.ListReviews(ctx, providerScope, options)
		case "review":
			result, err = client.GetReview(ctx, providerScope, input.ReviewID)
		case "pending_review":
			var identity github.AppIdentity
			if err := json.Unmarshal(access.Integration.ProviderIdentity, &identity); err != nil {
				return integrationToolFailure(errors.New("GitHub integration has no verified bot identity"))
			}
			var review *github.Review
			var pending github.Review
			var found bool
			pending, found, err = client.GetPendingReview(ctx, providerScope, identity.BotUserID)
			if found {
				review = &pending
			}
			result = struct {
				Review *github.Review `json:"review"`
			}{review}
		case "review_threads":
			result, err = client.ListReviewThreads(ctx, providerScope, github.ReviewThreadsOptions{
				Cursor: input.Cursor, Limit: input.Limit,
			})
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
	case toolcatalog.IntegrationOperationReviewComment:
		var input githubReviewCommentInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub review comment"); err != nil {
			return integrationToolFailure(err)
		}
		if (input.ReviewID == 0) == (input.CommitID == "") {
			return integrationToolFailure(errors.New(
				"supply exactly one of commit_id to publish immediately, or review_id to add to a pending review"))
		}
		if input.ReviewID != 0 {
			result, err = client.AddPendingReviewComment(ctx, providerScope, input.ReviewID, input.ReviewCommentArgs)
		} else {
			result, err = client.CreateReviewComment(ctx, providerScope, input.ReviewCommentArgs)
		}
	case toolcatalog.IntegrationOperationStartReview:
		var input githubStartReviewInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub start review"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.StartReview(ctx, providerScope, input.CommitID)
	case toolcatalog.IntegrationOperationSubmitReview:
		var input githubSubmitReviewInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub submit review"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.SubmitReview(ctx, providerScope, input.ReviewID, input.Body)
	case toolcatalog.IntegrationOperationDiscardReview:
		var input githubDiscardReviewInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub discard review"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.DiscardReview(ctx, providerScope, input.ReviewID)
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
		InstallationID:      installationID,
		HTTPClient:          e.IntegrationHTTPClient,
		CredentialSecretID:  access.Integration.CredentialSecretID,
		CredentialVersionID: access.CredentialVersion,
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
			result.Message = "GitHub may have accepted this operation. Read back the comment or review before retrying; " +
				"use the read tool to check the relevant comments or review state."
			if errors.Unwrap(apiError) != nil {
				result.Message = err.Error() + ". " + result.Message
			}
		}
	}
	content, marshalErr := structuredToolResultContent(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return failAsynchronously(content, err), nil
}
