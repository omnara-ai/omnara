package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func (s strictOpenAPIServer) ListIntegrationGitHubRepositories(
	ctx context.Context,
	request openapi.ListIntegrationGitHubRepositoriesRequestObject,
) (openapi.ListIntegrationGitHubRepositoriesResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	authority, err := s.server.githubIntegrationAuthority(ctx, scope.org.ID, scope.project.ID, id)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	client, err := s.server.githubIntegrationClient(ctx, scope.org.ID, scope.project.ID, authority)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	repositories, err := client.InstallationRepositories(ctx)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	response := openapi.ListIntegrationGitHubRepositories200JSONResponse{
		Repositories: make([]openapi.GitHubRepository, 0, len(repositories)),
	}
	for _, repository := range repositories {
		response.Repositories = append(response.Repositories,
			openapi.GitHubRepository{Id: repository.ID, Name: repository.Name})
	}
	return response, nil
}

func githubRepositoriesError(ctx context.Context, err error) error {
	return gitCredentialsError(ctx, err,
		"list GitHub repositories", "GitHub repositories are not available for this integration")
}
