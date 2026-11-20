package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type githubTargetInput struct {
	RepositoryID int64 `json:"repository_id,omitempty"`
	PullRequest  int   `json:"pull_request,omitempty"`
}

type githubReadInput struct {
	githubTargetInput
	Section  string `json:"section,omitempty"`
	Page     int    `json:"page,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	ReviewID int64  `json:"review_id,omitempty"`
}

type githubDiscussionInput struct {
	githubTargetInput
	Body string `json:"body"`
}

type githubReviewCommentInput struct {
	githubTargetInput
	github.ReviewCommentArgs
	ReviewID int64 `json:"review_id,omitempty"`
}

type githubStartReviewInput struct {
	githubTargetInput
	CommitID string `json:"commit_id"`
}

type githubSubmitReviewInput struct {
	githubTargetInput
	ReviewID int64  `json:"review_id"`
	Body     string `json:"body"`
}

type githubDiscardReviewInput struct {
	githubTargetInput
	ReviewID int64 `json:"review_id"`
}

type githubReplyInput struct {
	githubTargetInput
	CommentID int64  `json:"comment_id"`
	Body      string `json:"body"`
}

type githubResolveThreadInput struct {
	githubTargetInput
	ReviewThreadID string `json:"review_thread_id"`
}

func runGitHubTool(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access integrationToolAccess,
) (asyncPhaseResult, error) {
	switch access.Authority.Definition.Operation {
	case toolcatalog.IntegrationOperationAttachPullRequest, toolcatalog.IntegrationOperationDetachPullRequest:
		return runGitHubAttachment(ctx, call, record, access)
	case toolcatalog.IntegrationOperationListAttachedPullRequests:
		return listGitHubAttachedPullRequests(ctx, call, record, access)
	}
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
	case toolcatalog.IntegrationOperationResolveThread:
		var input githubResolveThreadInput
		if err := decodeSingleStrictJSON(record.Input, &input, "GitHub resolve thread"); err != nil {
			return integrationToolFailure(err)
		}
		result, err = client.ResolveReviewThread(ctx, providerScope, input.ReviewThreadID)
	default:
		return nil, fmt.Errorf("unsupported GitHub tool \"%s\"", record.Name)
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

func runGitHubAttachment(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access integrationToolAccess,
) (asyncPhaseResult, error) {
	var input integrationdefinition.GitHubScope
	if err := decodeSingleStrictJSON(record.Input, &input, record.Name); err != nil {
		return integrationToolFailure(err)
	}
	attachment := integrationstore.AgentConversationInput{
		CreateIntegrationSubscriptionInput: integrationstore.CreateIntegrationSubscriptionInput{
			OrgID: call.Turn.OrgID, ProjectID: call.Turn.ProjectID, IntegrationID: access.Integration.ID,
			AgentID: call.Turn.AgentID, Conversation: record.Input,
		},
		ExpectedConfigID: access.CurrentConfigID,
		RuntimeLockID:    call.Turn.RuntimeLockID,
	}
	var url string
	var err error
	if access.Authority.Definition.Operation == toolcatalog.IntegrationOperationDetachPullRequest {
		err = call.Executor.Store.Integrations().DetachAgentConversation(ctx, attachment)
	} else {
		client, clientErr := call.Executor.githubToolClient(call.Turn, record, access)
		if clientErr != nil {
			return integrationToolFailure(clientErr)
		}
		pull, pullErr := client.GetPullRequest(ctx,
			github.Scope{RepositoryID: input.RepositoryID, PullRequest: input.PullRequest})
		if pullErr != nil {
			return integrationToolFailure(pullErr)
		}
		url = pull.HTMLURL
		err = call.Executor.Store.Integrations().AttachAgentConversation(ctx, attachment)
	}
	if errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		return nil, err
	}
	if errors.Is(err, integrationstore.ErrAgentConfigChanged) {
		return integrationToolFailure(
			fmt.Errorf("%w: agent config changed; submit a new call", ErrToolAuthorizationInvalidated))
	}
	if errors.Is(err, storeerr.ErrUnauthorized) {
		return integrationToolFailure(
			fmt.Errorf("%w: integration access changed; submit a new call", ErrToolAuthorizationInvalidated))
	}
	if err != nil {
		return integrationToolFailure(err)
	}
	content, err := structuredToolResultContent(struct {
		RepositoryID int64  `json:"repository_id"`
		PullRequest  int    `json:"pull_request"`
		URL          string `json:"url,omitempty"`
	}{input.RepositoryID, input.PullRequest, url})
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func listGitHubAttachedPullRequests(
	ctx context.Context,
	call asyncToolContext,
	record executionstore.ToolCallRecord,
	access integrationToolAccess,
) (asyncPhaseResult, error) {
	var input struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	if err := decodeSingleStrictJSON(record.Input, &input, record.Name); err != nil {
		return integrationToolFailure(err)
	}
	if input.Limit == 0 {
		input.Limit = toolcatalog.ListAttachedPullRequestsDefaultLimit
	}
	pullRequests, err := call.Executor.listAgentGitHubPullRequests(ctx, call.Turn, access.Integration)
	if err != nil {
		return nil, err
	}
	start, end := min(input.Offset, len(pullRequests)), listing.PageEnd(input.Offset, input.Limit, len(pullRequests))
	content, err := structuredToolResultContent(struct {
		PullRequests []integrationdefinition.GitHubScope `json:"pull_requests"`
		listing.OffsetPage
	}{pullRequests[start:end], listing.ContinueAt(end, end < len(pullRequests))})
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
	return e.githubClient(access.Integration, access.Credential, access.CredentialVersion,
		func(ctx context.Context) error {
			return e.recheckIntegrationToolAccess(ctx, turn, tool, access)
		})
}

func (e Executor) githubClient(
	integration integrationstore.IntegrationRecord,
	credential secrets.Payload,
	credentialVersion uuid.UUID,
	beforeRequest func(context.Context) error,
) (*github.Client, error) {
	appID, err := strconv.ParseInt(credential[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 || integration.ProviderTenantID != strconv.FormatInt(appID, 10) {
		return nil, errors.New("GitHub App credentials do not match the integration")
	}
	installationID, err := strconv.ParseInt(integration.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 {
		return nil, errors.New("GitHub installation identity is invalid")
	}
	return github.NewClient(github.Config{
		Credentials: github.Credentials{
			AppID:         appID,
			PrivateKeyPEM: credential[secrets.KeyPrivateKey],
			WebhookSecret: credential[secrets.KeyWebhookSecret],
		},
		InstallationID:      installationID,
		HTTPClient:          e.IntegrationHTTPClient,
		CredentialSecretID:  integration.CredentialSecretID,
		CredentialVersionID: credentialVersion,
		BeforeRequest:       beforeRequest,
	})
}

func integrationToolFailure(err error) (asyncPhaseResult, error) {
	result := struct {
		Code              string `json:"error_code"`
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
