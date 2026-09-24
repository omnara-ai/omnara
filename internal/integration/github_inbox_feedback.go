package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type GitHubInboxSecrets interface {
	ReadProjectAvailableSecretPayload(context.Context, secretstore.ReadProjectAvailableSecretPayloadInput) (
		secretstore.SecretPayloadRecord, error,
	)
	GetProjectAvailableSecret(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (
		secretstore.ProjectSecretAccessRecord, error,
	)
}

type GitHubInboxIntegrations interface {
	GetProjectIntegration(context.Context, uuid.UUID, uuid.UUID) (integrationstore.ProjectIntegrationRecord, error)
}

func NewGitHubIntegrationInboxProvider(config github.Config, secrets GitHubInboxSecrets,
	integrations GitHubInboxIntegrations,
) *GitHubIntegrationInboxProvider {
	config.Credentials, config.InstallationID = github.Credentials{}, 0
	return &GitHubIntegrationInboxProvider{config: config, secrets: secrets, integrations: integrations}
}

func (p GitHubIntegrationInboxProvider) NotifyInboxFailure(ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord, receipt integrationstore.IntegrationInboxRecord, text string,
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
	if event.Event.Kind != "discussion_comment" && event.Event.Kind != "review_comment" {
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

func (p GitHubIntegrationInboxProvider) requestAccess(ctx context.Context,
	integration integrationstore.ProjectIntegrationRecord,
) (*github.Client, error) {
	if p.integrations == nil || p.secrets == nil {
		return nil, fmt.Errorf("GitHub secret and integration resolvers are required")
	}
	checkIntegration := func(ctx context.Context) error {
		latest, err := p.integrations.GetProjectIntegration(ctx, integration.ProjectID, integration.ID)
		if err != nil {
			return err
		}
		if latest.State != integrationstore.ProjectIntegrationStateActive ||
			latest.Provider != integrationdefinition.ProviderGitHub ||
			latest.ID != integration.ID || latest.ProjectID != integration.ProjectID || latest.OrgID != integration.OrgID ||
			latest.ProviderTenantID != integration.ProviderTenantID ||
			latest.ProviderAccountRef != integration.ProviderAccountRef ||
			latest.CredentialSecretID != integration.CredentialSecretID || latest.SetupRevision != integration.SetupRevision {
			return storeerr.ErrUnauthorized
		}
		return nil
	}
	if err := checkIntegration(ctx); err != nil {
		return nil, err
	}
	credential, err := p.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID:     integration.OrgID,
		ProjectID: integration.ProjectID,
		SecretID:  integration.CredentialSecretID,
		Kind:      secrets.KindGitHubAppCredentials,
	})
	if err != nil {
		return nil, err
	}
	appID, err := strconv.ParseInt(credential.Payload[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 || strconv.FormatInt(appID, 10) != integration.ProviderTenantID {
		return nil, storeerr.ErrUnauthorized
	}
	installationID, err := strconv.ParseInt(integration.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 || strconv.FormatInt(installationID, 10) != integration.ProviderAccountRef {
		return nil, storeerr.ErrUnauthorized
	}
	config := p.config
	config.Credentials = github.Credentials{AppID: appID,
		PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey], WebhookSecret: credential.Payload[secrets.KeyWebhookSecret]}
	config.InstallationID = installationID
	config.BeforeRequest = func(ctx context.Context) error {
		access, err := p.secrets.GetProjectAvailableSecret(
			ctx,
			integration.OrgID,
			integration.ProjectID,
			integration.CredentialSecretID,
		)
		if err != nil {
			return err
		}
		if credential.CurrentVersionID == uuid.Nil || access.Secret.Kind != secrets.KindGitHubAppCredentials ||
			access.Secret.CurrentVersionID != credential.CurrentVersionID {
			return storeerr.ErrUnauthorized
		}
		if err := checkIntegration(ctx); err != nil {
			return err
		}
		if p.config.BeforeRequest != nil {
			return p.config.BeforeRequest(ctx)
		}
		return nil
	}
	if err := config.BeforeRequest(ctx); err != nil {
		return nil, err
	}
	return github.NewClient(config)
}
