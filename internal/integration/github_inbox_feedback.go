package integration

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (p GitHubIntegrationInboxProvider) NotifyInboxFailure(ctx context.Context,
	integration integrationstore.IntegrationRecord, receipt integrationstore.IntegrationInboxRecord, text string,
) error {
	if err := checkInboxFailureReceipt(integration, receipt, integrationdefinition.ProviderGitHub); err != nil {
		return err
	}
	if receipt.Source != integrationstore.IntegrationInboxSourceProvider {
		return nil
	}
	event, ok, err := NormalizeGitHubIntegrationEvent(integration, receipt.Payload)
	if err != nil || !ok {
		return err
	}
	if event.Event.Kind != integrationdefinition.EventDiscussionComment &&
		event.Event.Kind != integrationdefinition.EventReviewComment {
		return nil
	}
	if len(receipt.Plan) == 0 && !event.Event.Mentioned {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, github.OperationTimeout)
	defer cancel()
	client, err := p.requestAccess(ctx, integration)
	if err != nil {
		return err
	}
	if len(receipt.Plan) == 0 {
		allowed, err := githubEventSenderAllowed(ctx, client, event)
		if err != nil || !allowed {
			return err
		}
	}
	scope := github.Scope{RepositoryID: event.Event.Scope.GitHub.RepositoryID,
		PullRequest: event.Event.Scope.GitHub.PullRequest}
	var metadata GitHubEventMetadata
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
		return err
	}
	if metadata.EventType == "pull_request_review_comment" {
		_, err = client.Reply(ctx, scope, metadata.CommentID, text)
	} else {
		_, err = client.CreateDiscussionComment(ctx, scope, text)
	}
	return err
}

func (p GitHubIntegrationInboxProvider) NotifyLaunchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, message string,
) error {
	scope := input.Event.Event.Scope.GitHub
	if scope == nil || !input.Event.Event.Mentioned {
		return nil
	}
	client, err := p.requestAccess(ctx, input.Integration)
	if err != nil {
		return err
	}
	_, err = client.CreateDiscussionComment(ctx, github.Scope{
		RepositoryID: scope.RepositoryID, PullRequest: scope.PullRequest,
	}, message)
	return err
}
