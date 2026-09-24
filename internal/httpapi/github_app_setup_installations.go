package httpapi

import (
	"context"
	"net/url"
	"strconv"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func (s strictOpenAPIServer) InspectProjectIntegrationGitHubInstallations(
	ctx context.Context,
	request openapi.InspectProjectIntegrationGitHubInstallationsRequestObject,
) (openapi.InspectProjectIntegrationGitHubInstallationsResponseObject, error) {
	if err := authorizeOperationPrincipal(ctx, principalKindBrowserSession); err != nil {
		return nil, err
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	integration, err := s.projectIntegrationForSetup(ctx, scope, request.IntegrationID)
	if err != nil {
		return nil, err
	}
	if err := s.server.validateGitHubGuidedSetup(); err != nil {
		return nil, err
	}
	if integration.IntegrationType != integrationdefinition.GitHubPR {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "this integration does not support GitHub setup")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	secretID, err := publicid.Decode(publicid.KindSecret, request.Body.CredentialsSecretRef)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid credentials_secret_ref")
	}
	page := 1
	if request.Body.Page != nil {
		page = *request.Body.Page
	}
	if page < 1 || page > 1000 {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "page must be between 1 and 1000")
	}
	credential, err := s.server.store.Secrets().
		ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     integration.OrgID,
			ProjectID: integration.ProjectID,
			SecretID:  secretID,
			Kind:      secrets.KindGitHubAppCredentials,
		})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	appID, err := strconv.ParseInt(credential.Payload[secrets.KeyAppID], 10, 64)
	if err != nil || appID <= 0 {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid GitHub credential App ID")
	}
	if integration.ProviderTenantID != "" && integration.ProviderTenantID != strconv.FormatInt(appID, 10) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "credentials belong to another GitHub App")
	}
	config := github.SetupConfig{
		APIURL: s.server.githubClientConfig.APIURL, HTTPClient: s.server.githubClientConfig.HTTPClient,
		BeforeRequest: s.server.githubClientConfig.BeforeRequest,
	}
	config.Credentials = github.Credentials{
		AppID:         appID,
		PrivateKeyPEM: credential.Payload[secrets.KeyPrivateKey],
		WebhookSecret: credential.Payload[secrets.KeyWebhookSecret],
	}
	client, err := github.NewSetupClient(config)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid GitHub App credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, github.OperationTimeout)
	defer cancel()
	metadata, err := client.App(ctx)
	if err != nil {
		return nil, integrationSetupInputError(err)
	}
	installations, err := client.ListInstallations(ctx, github.PageOptions{Page: page, PerPage: 100})
	if err != nil {
		return nil, integrationSetupInputError(err)
	}
	response := openapi.GitHubInstallations{
		ProviderAppId: strconv.FormatInt(metadata.ID, 10),
		Name:          metadata.Name,
		Slug:          metadata.Slug,
		InstallUrl: "https://github.com/apps/" + url.PathEscape(metadata.Slug) + "/installations/new?" +
			url.Values{"state": {request.Body.CredentialsSecretRef}}.Encode(),
		Installations: make([]openapi.GitHubSetupInstallation, 0, len(installations.Installations)),
	}
	for _, installation := range installations.Installations {
		id := strconv.FormatInt(installation.ID, 10)
		settingsURL := "https://github.com/settings/installations/" + id
		if installation.Account.Type == "Organization" {
			settingsURL = "https://github.com/organizations/" + url.PathEscape(installation.Account.Login) +
				"/settings/installations/" + id
		}
		item := openapi.GitHubSetupInstallation{Id: id, Account: installation.Account.Login, SettingsUrl: settingsURL}
		if installation.Account.Type != "" {
			item.AccountType = &installation.Account.Type
		}
		response.Installations = append(response.Installations, item)
	}
	if installations.NextPage > 0 {
		response.NextPage = &installations.NextPage
	}
	return openapi.InspectProjectIntegrationGitHubInstallations200JSONResponse(response), nil
}
