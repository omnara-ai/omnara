package httpapi

import (
	"context"
	"strconv"

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
	page := 1
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		if page, err = strconv.Atoi(*request.Params.Cursor); err != nil || page < 2 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, errMalformedCursor.Error())
		}
	}
	authority, err := s.server.githubIntegrationAuthority(ctx, scope.org.ID, scope.project.ID, id)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	client, err := s.server.githubIntegrationClient(ctx, scope.org.ID, scope.project.ID, authority)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	repositories, nextPage, err := client.InstallationRepositories(ctx, page)
	if err != nil {
		return nil, githubRepositoriesError(ctx, err)
	}
	response := openapi.ListIntegrationGitHubRepositories200JSONResponse{
		Data: make([]openapi.GitHubRepository, 0, len(repositories)),
	}
	for _, repository := range repositories {
		response.Data = append(response.Data, openapi.GitHubRepository{Id: repository.ID, Name: repository.Name})
	}
	var next *string
	if nextPage != 0 {
		cursor := strconv.Itoa(nextPage)
		next = &cursor
	}
	response.NextCursor = nullableFromPtr(next)
	return response, nil
}

func githubRepositoriesError(ctx context.Context, err error) error {
	return gitCredentialsError(ctx, err,
		"list GitHub repositories", "GitHub repositories are not available for this integration")
}
