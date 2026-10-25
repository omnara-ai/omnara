package httpapi

import (
	"context"
	"errors"
	"reflect"
	"strconv"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type githubIntegrationAuthority struct {
	CredentialSecretID  uuid.UUID
	CredentialVersionID uuid.UUID
	AppID               int64
	InstallationID      int64
	CodeWrite           bool
}

type daemonGitCredentialsAuthority struct {
	Process executionstore.DaemonGitCredentialsScope
	githubIntegrationAuthority
}

func (s strictOpenAPIServer) GetDaemonGitCredentials(
	ctx context.Context,
	request openapi.GetDaemonGitCredentialsRequestObject,
) (openapi.GetDaemonGitCredentialsResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	processID, ok := parseOpenAPIPublicID(publicid.KindProcess, request.ProcessID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	authority, err := s.server.daemonGitCredentialsAuthority(ctx, scope, processID)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	client, err := s.server.githubIntegrationClient(
		ctx, scope.OrgID, authority.Process.ProjectID, authority.githubIntegrationAuthority,
	)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	result, err := client.InstallationGitCredentials(
		ctx, authority.CodeWrite && !authority.Process.ReadOnly, authority.Process.RepositoryIDs,
	)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	current, err := s.server.daemonGitCredentialsAuthority(ctx, scope, processID)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	if !reflect.DeepEqual(current, authority) {
		return nil, daemonGitCredentialsError(ctx, storeerr.ErrNotFound)
	}
	response := openapi.GetDaemonGitCredentials200JSONResponse{
		Headers: openapi.GetDaemonGitCredentials200ResponseHeaders{CacheControl: "no-store"},
	}
	response.Body.Token = result.Token
	response.Body.ExpiresAt = result.ExpiresAt
	return response, nil
}

func (s *Server) daemonGitCredentialsAuthority(
	ctx context.Context, scope machineDaemonScope, processID uuid.UUID,
) (daemonGitCredentialsAuthority, error) {
	process, found, err := s.store.Execution().GetDaemonGitCredentialsScope(ctx, scope.OrgID, scope.MachineID, processID)
	if err != nil {
		return daemonGitCredentialsAuthority{}, err
	}
	if !found {
		return daemonGitCredentialsAuthority{}, storeerr.ErrNotFound
	}
	integration, err := s.githubIntegrationAuthority(ctx, scope.OrgID, process.ProjectID, process.IntegrationID)
	if err != nil {
		return daemonGitCredentialsAuthority{}, err
	}
	return daemonGitCredentialsAuthority{Process: process, githubIntegrationAuthority: integration}, nil
}

func (s *Server) githubIntegrationAuthority(
	ctx context.Context, orgID, projectID, integrationID uuid.UUID,
) (githubIntegrationAuthority, error) {
	integration, err := s.store.Integrations().GetIntegration(ctx, projectID, integrationID)
	if err != nil {
		return githubIntegrationAuthority{}, err
	}
	definition, ok := integrationdefinition.Lookup(integration.IntegrationKind)
	if !ok || definition.Provider != integrationdefinition.ProviderGitHub ||
		integration.OrgID != orgID || integration.State != integrationstore.IntegrationStateActive ||
		integration.Provider != integrationdefinition.ProviderGitHub || integration.CredentialSecretID == uuid.Nil {
		return githubIntegrationAuthority{}, storeerr.ErrNotFound
	}
	appID, err := strconv.ParseInt(integration.ProviderTenantID, 10, 64)
	if err != nil || appID <= 0 {
		return githubIntegrationAuthority{}, storeerr.ErrNotFound
	}
	installationID, err := strconv.ParseInt(integration.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 {
		return githubIntegrationAuthority{}, storeerr.ErrNotFound
	}
	settings, err := integrationdefinition.ReadGitHubSettings(integration.Settings)
	if err != nil {
		return githubIntegrationAuthority{}, err
	}
	access, err := s.store.Secrets().GetProjectAvailableSecret(ctx, orgID, projectID, integration.CredentialSecretID)
	if err != nil {
		return githubIntegrationAuthority{}, err
	}
	if access.Secret.Kind != secrets.KindGitHubAppCredentials {
		return githubIntegrationAuthority{}, storeerr.ErrNotFound
	}
	return githubIntegrationAuthority{
		CredentialSecretID: integration.CredentialSecretID, CredentialVersionID: access.Secret.CurrentVersionID,
		AppID: appID, InstallationID: installationID, CodeWrite: settings.CodeWrite,
	}, nil
}

func (s *Server) githubIntegrationClient(
	ctx context.Context, orgID, projectID uuid.UUID, authority githubIntegrationAuthority,
) (*github.Client, error) {
	credential, err := s.store.Secrets().ReadProjectAvailableSecretPayload(ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: orgID, ProjectID: projectID,
			SecretID: authority.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
		})
	if err != nil {
		return nil, err
	}
	credentialAppID, err := strconv.ParseInt(credential.Payload[secrets.KeyAppID], 10, 64)
	if err != nil || credentialAppID != authority.AppID || credential.CurrentVersionID != authority.CredentialVersionID {
		return nil, storeerr.ErrNotFound
	}
	return s.gitCredentialClients.Client(
		authority.CredentialSecretID, authority.CredentialVersionID, authority.InstallationID,
		github.Credentials{
			AppID: authority.AppID, PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey],
			WebhookSecret: credential.Payload[secrets.KeyWebhookSecret],
		},
	)
}

func daemonGitCredentialsError(ctx context.Context, err error) error {
	return gitCredentialsError(ctx, err,
		"issue daemon Git credentials", "Git credentials are not available for this process")
}

func gitCredentialsError(ctx context.Context, err error, operation, notFound string) error {
	if errors.Is(err, storeerr.ErrNotFound) {
		return apierror.FromCode(openapi.ErrorCodeNotFound, notFound)
	}
	if errors.Is(err, github.ErrContentsPermissionRequired) {
		return apierror.FromCode(openapi.ErrorCodeConflict, github.ErrContentsPermissionRequired.Error())
	}
	var providerErr *github.APIError
	if errors.As(err, &providerErr) &&
		(providerErr.Code == github.PermanentFailure || providerErr.Code == github.ScopeMismatch) {
		return apierror.FromCode(openapi.ErrorCodeConflict,
			"Git credentials are unavailable; check the GitHub App, installation permissions and repositories")
	}
	logIntegrationError(ctx, operation, err)
	return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "Git credentials are temporarily unavailable")
}
