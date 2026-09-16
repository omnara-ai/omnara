package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) ListGitHubSetupInstallations(
	ctx context.Context, request openapi.ListGitHubSetupInstallationsRequestObject,
) (openapi.ListGitHubSetupInstallationsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, integrationOAuthTimeout)
	defer cancel()
	session, client, err := s.githubSetupSession(ctx, request.FlowID)
	if err != nil {
		return nil, err
	}
	app, err := client.VerifyApp(ctx)
	if err != nil {
		return nil, githubSetupAPIError(err)
	}
	page, err := client.ListInstallations(ctx, githubSetupUser(session), githubSetupPage(request.Params.Page))
	if err != nil {
		return nil, githubSetupAPIError(err)
	}
	response := openapi.ListGitHubSetupInstallations200JSONResponse{
		Data: make([]openapi.GitHubSetupInstallation, 0, len(page.Installations)), InstallationUrl: app.InstallationURL,
	}
	for _, install := range page.Installations {
		response.Data = append(response.Data, openapi.GitHubSetupInstallation{
			Id: install.ID, AccountLogin: install.Account.Login, Suspended: install.Suspended,
		})
	}
	if page.NextPage != 0 {
		response.NextPage = &page.NextPage
	}
	return response, nil
}

func (s strictOpenAPIServer) ListGitHubSetupRepositories(
	ctx context.Context, request openapi.ListGitHubSetupRepositoriesRequestObject,
) (openapi.ListGitHubSetupRepositoriesResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, integrationOAuthTimeout)
	defer cancel()
	session, client, err := s.githubSetupSession(ctx, request.FlowID)
	if err != nil {
		return nil, err
	}
	page, err := client.ListRepositories(ctx, githubSetupUser(session),
		request.ProviderInstallationID, githubSetupPage(request.Params.Page))
	if err != nil {
		return nil, githubSetupAPIError(err)
	}
	response := openapi.ListGitHubSetupRepositories200JSONResponse{
		Data: make([]openapi.GitHubSetupRepository, 0, len(page.Repositories)),
	}
	for _, repo := range page.Repositories {
		response.Data = append(response.Data, openapi.GitHubSetupRepository{
			Id: repo.ID, FullName: repo.FullName, Private: repo.Private, CanConnect: repo.Admin,
		})
	}
	if page.NextPage != 0 {
		response.NextPage = &page.NextPage
	}
	return response, nil
}

func (s strictOpenAPIServer) CompleteGitHubConnection(
	ctx context.Context, request openapi.CompleteGitHubConnectionRequestObject,
) (openapi.CompleteGitHubConnectionResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	ctx, cancel := context.WithTimeout(ctx, integrationOAuthTimeout)
	defer cancel()
	session, client, err := s.githubSetupSession(ctx, request.FlowID)
	if err != nil {
		return nil, err
	}
	selection, err := client.VerifySelection(
		ctx, githubSetupUser(session), request.Body.InstallationId,
		request.Body.RepositoryId, request.Body.RepositoryFullName,
	)
	if err != nil {
		return nil, githubSetupAPIError(err)
	}
	state := session.State
	if _, err := s.server.loadIntegrationSetupSession(ctx,
		state.ProjectID, state.InstalledByUserID, state.FlowID, true); err != nil {
		return nil, integrationSetupSessionError(err)
	}
	input, err := githubInstallationInput(state, selection)
	if err != nil {
		return nil, err
	}
	install, err := s.server.saveManagedInstallation(ctx, state, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationInstallResponse(install)
	return openapi.CompleteGitHubConnection200JSONResponse(response), err
}

func (s strictOpenAPIServer) githubSetupSession(
	ctx context.Context, rawFlowID string,
) (integrationSetupSession, *github.Client, error) {
	var session integrationSetupSession
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return session, nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return session, nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	flowID, err := publicid.Decode(publicid.KindIntegrationOAuthFlow, rawFlowID)
	if err != nil {
		return session, nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	session, err = s.server.loadIntegrationSetupSession(ctx, scope.project.ID, principal.ID, flowID, false)
	if err != nil {
		return session, nil, integrationSetupSessionError(err)
	}
	state := session.State
	if state.OrgID != scope.project.OrgID || state.Provider != integrationstore.IntegrationProviderGitHub ||
		state.IntegrationAppID == uuid.Nil {
		return session, nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "invalid integration setup session")
	}
	if err := s.server.requireIntegrationGateway(state.Provider); err != nil {
		return session, nil, err
	}
	app, payload, err := s.server.integrationSetupApp(ctx, state.OrgID, state.ProjectID,
		state.IntegrationAppID, state.AppConfigurationRevision)
	if err != nil {
		return session, nil, err
	}
	client, err := s.server.githubSetupClient(app, payload)
	return session, client, err
}

func githubSetupUser(session integrationSetupSession) github.UserOAuth {
	return github.UserOAuth{AccessToken: session.UserToken, ExpiresAt: session.State.ExpiresAt}
}

func githubSetupPage(page *int) int {
	if page == nil {
		return 1
	}
	return *page
}

func integrationSetupSessionError(err error) error {
	if errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed) {
		return apierror.FromCode(openapi.ErrorCodeUnauthorized,
			"integration setup session expired or already completed; connect again")
	}
	return err
}

func githubSetupAPIError(err error) error {
	switch {
	case errors.Is(err, github.ErrRepositoryAdminRequired):
		return apierror.FromCode(openapi.ErrorCodeForbidden, "GitHub repository administrator access is required to connect")
	case errors.Is(err, github.ErrSelectionNotAccessible), errors.Is(err, github.ErrInstallationUnavailable):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "GitHub installation or repository is no longer accessible")
	case errors.Is(err, github.ErrMissingPermissions):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"GitHub app requires pull requests write, issues read, and metadata read permissions")
	case errors.Is(err, github.ErrOAuthExpired):
		return apierror.FromCode(openapi.ErrorCodeUnauthorized, "GitHub setup authorization expired; connect again")
	case errors.Is(err, github.ErrUnsupportedID):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "GitHub identifier exceeds the supported range")
	case errors.Is(err, github.ErrInvalidInput):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid GitHub setup selection")
	}
	var native *github.APIError
	if errors.As(err, &native) && native.StatusCode == http.StatusUnauthorized {
		return apierror.FromCode(openapi.ErrorCodeUnauthorized,
			"GitHub setup authorization is no longer valid; connect again")
	}
	return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "GitHub setup verification failed; try again")
}
