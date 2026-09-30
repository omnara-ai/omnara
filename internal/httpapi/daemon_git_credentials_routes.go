package httpapi

import (
	"context"
	"errors"
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

type daemonGitCredentialsAuthority struct {
	Process             executionstore.DaemonGitCredentialsScope
	CredentialSecretID  uuid.UUID
	CredentialVersionID uuid.UUID
	AppID               int64
	InstallationID      int64
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
	credential, err := s.server.store.Secrets().ReadProjectAvailableSecretPayload(ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: scope.OrgID, ProjectID: authority.Process.ProjectID,
			SecretID: authority.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
		})
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	credentialAppID, err := strconv.ParseInt(credential.Payload[secrets.KeyAppID], 10, 64)
	if err != nil || credentialAppID != authority.AppID || credential.CurrentVersionID != authority.CredentialVersionID {
		return nil, daemonGitCredentialsError(ctx, storeerr.ErrNotFound)
	}
	client, err := s.server.gitCredentialClients.Client(
		authority.CredentialSecretID, authority.CredentialVersionID, authority.InstallationID,
		github.Credentials{
			AppID: authority.AppID, PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey],
			WebhookSecret: credential.Payload[secrets.KeyWebhookSecret],
		},
	)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	result, err := client.InstallationGitCredentials(ctx)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	// Recheck after issuance and cache hits alike; never return a token under stale authority.
	current, err := s.server.daemonGitCredentialsAuthority(ctx, scope, processID)
	if err != nil {
		return nil, daemonGitCredentialsError(ctx, err)
	}
	if current != authority {
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
	integration, err := s.store.Integrations().GetIntegration(ctx, process.ProjectID, process.IntegrationID)
	if err != nil {
		return daemonGitCredentialsAuthority{}, err
	}
	definition, ok := integrationdefinition.Lookup(integration.IntegrationKind)
	if !ok || definition.Provider != integrationdefinition.ProviderGitHub ||
		integration.OrgID != scope.OrgID || integration.State != integrationstore.IntegrationStateActive ||
		integration.Provider != integrationstore.IntegrationProviderGitHub || integration.CredentialSecretID == uuid.Nil {
		return daemonGitCredentialsAuthority{}, storeerr.ErrNotFound
	}
	appID, err := strconv.ParseInt(integration.ProviderTenantID, 10, 64)
	if err != nil || appID <= 0 {
		return daemonGitCredentialsAuthority{}, storeerr.ErrNotFound
	}
	installationID, err := strconv.ParseInt(integration.ProviderAccountRef, 10, 64)
	if err != nil || installationID <= 0 {
		return daemonGitCredentialsAuthority{}, storeerr.ErrNotFound
	}
	access, err := s.store.Secrets().GetProjectAvailableSecret(
		ctx, scope.OrgID, process.ProjectID, integration.CredentialSecretID,
	)
	if err != nil {
		return daemonGitCredentialsAuthority{}, err
	}
	if access.Secret.Kind != secrets.KindGitHubAppCredentials {
		return daemonGitCredentialsAuthority{}, storeerr.ErrNotFound
	}
	return daemonGitCredentialsAuthority{
		Process: process, CredentialSecretID: integration.CredentialSecretID,
		CredentialVersionID: access.Secret.CurrentVersionID, AppID: appID, InstallationID: installationID,
	}, nil
}

func daemonGitCredentialsError(ctx context.Context, err error) error {
	if errors.Is(err, storeerr.ErrNotFound) {
		return apierror.FromCode(openapi.ErrorCodeNotFound, "Git credentials are not available for this process")
	}
	if errors.Is(err, github.ErrContentsPermissionRequired) {
		return apierror.FromCode(openapi.ErrorCodeConflict, github.ErrContentsPermissionRequired.Error())
	}
	var providerErr *github.APIError
	if errors.As(err, &providerErr) &&
		(providerErr.Code == github.PermanentFailure || providerErr.Code == github.ScopeMismatch) {
		return apierror.FromCode(openapi.ErrorCodeConflict,
			"Git credentials are unavailable; check the GitHub App and installation permissions")
	}
	logIntegrationCredentialError(ctx, "issue daemon Git credentials", err)
	return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "Git credentials are temporarily unavailable")
}
