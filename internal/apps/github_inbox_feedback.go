package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
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

type GitHubInboxApps interface {
	GetProjectApp(context.Context, uuid.UUID, uuid.UUID) (appstore.ProjectAppRecord, error)
}

func NewGitHubAppInboxProvider(config github.Config, secrets GitHubInboxSecrets,
	apps GitHubInboxApps,
) *GitHubAppInboxProvider {
	config.Credentials, config.InstallationID = github.Credentials{}, 0
	return &GitHubAppInboxProvider{config: config, secrets: secrets, apps: apps}
}

func (p GitHubAppInboxProvider) NotifyInboxFailure(ctx context.Context,
	app appstore.ProjectAppRecord, receipt appstore.AppInboxRecord, text string,
) error {
	if err := checkInboxFailureReceipt(app, receipt, appdefinition.ProviderGitHub); err != nil {
		return err
	}
	if receipt.Source != appstore.AppInboxSourceProvider {
		return nil
	}
	event, ok, err := NormalizeGitHubAppEvent(app, receipt.Payload)
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
	client, err := p.requestAccess(ctx, app)
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

func (p GitHubAppInboxProvider) requestAccess(ctx context.Context,
	app appstore.ProjectAppRecord,
) (*github.Client, error) {
	if p.apps == nil || p.secrets == nil {
		return nil, fmt.Errorf("GitHub secret and app resolvers are required")
	}
	checkApp := func(ctx context.Context) error {
		latest, err := p.apps.GetProjectApp(ctx, app.ProjectID, app.ID)
		if err != nil {
			return err
		}
		if latest.State != appstore.ProjectAppStateActive || latest.Provider != appdefinition.ProviderGitHub ||
			latest.ID != app.ID || latest.ProjectID != app.ProjectID || latest.OrgID != app.OrgID ||
			latest.ProviderTenantID != app.ProviderTenantID || latest.ProviderAccountRef != app.ProviderAccountRef ||
			latest.CredentialSecretID != app.CredentialSecretID || latest.SetupRevision != app.SetupRevision {
			return storeerr.ErrUnauthorized
		}
		return nil
	}
	if err := checkApp(ctx); err != nil {
		return nil, err
	}
	credential, err := p.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: app.OrgID, ProjectID: app.ProjectID, SecretID: app.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
	})
	if err != nil {
		return nil, err
	}
	appID, err := strconv.ParseInt(credential.Payload[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 || strconv.FormatInt(appID, 10) != app.ProviderTenantID {
		return nil, storeerr.ErrUnauthorized
	}
	installationID, err := strconv.ParseInt(app.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 || strconv.FormatInt(installationID, 10) != app.ProviderAccountRef {
		return nil, storeerr.ErrUnauthorized
	}
	config := p.config
	config.Credentials = github.Credentials{AppID: appID,
		PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey], WebhookSecret: credential.Payload[secrets.KeyWebhookSecret]}
	config.InstallationID = installationID
	config.BeforeRequest = func(ctx context.Context) error {
		access, err := p.secrets.GetProjectAvailableSecret(ctx, app.OrgID, app.ProjectID, app.CredentialSecretID)
		if err != nil {
			return err
		}
		if credential.CurrentVersionID == uuid.Nil || access.Secret.Kind != secrets.KindGitHubAppCredentials ||
			access.Secret.CurrentVersionID != credential.CurrentVersionID {
			return storeerr.ErrUnauthorized
		}
		if err := checkApp(ctx); err != nil {
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
