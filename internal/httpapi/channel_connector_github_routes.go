package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) LookupChannelConnectorGitHubReviews(
	ctx context.Context,
	request openapi.LookupChannelConnectorGitHubReviewsRequestObject,
) (openapi.LookupChannelConnectorGitHubReviewsResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := githubReviewConnectorScope(
		ctx, request.IntegrationAppID, request.IntegrationInstallID, request.Body.Scope,
	)
	if err != nil {
		return nil, err
	}
	observations := make([]executionstore.GitHubReviewObservation, 0, len(request.Body.Observations))
	for _, body := range request.Body.Observations {
		observation, err := githubReviewObservationInput(body)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	results, err := s.server.store.Execution().LookupGitHubReviewObservations(ctx, scope, observations)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response := openapi.LookupChannelConnectorGitHubReviews200JSONResponse{
		Observations: make([]openapi.GitHubReviewOwnershipObservation, 0, len(results)),
	}
	for _, result := range results {
		item := openapi.GitHubReviewOwnershipObservation{
			ReviewId: result.ReviewID, Ownership: openapi.GitHubReviewOwnership(result.Ownership),
		}
		if result.Ownership == executionstore.GitHubReviewOwned {
			item.CreatingToolCallId, err = idOrNil(publicid.KindToolCall, result.CreatingToolCallID)
			if err != nil {
				return nil, err
			}
			item.CommitId = &result.CommitID
		}
		response.Observations = append(response.Observations, item)
	}
	return response, nil
}

func (s strictOpenAPIServer) RecordChannelConnectorGitHubReview(
	ctx context.Context,
	request openapi.RecordChannelConnectorGitHubReviewRequestObject,
) (openapi.RecordChannelConnectorGitHubReviewResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := githubReviewConnectorScope(
		ctx, request.IntegrationAppID, request.IntegrationInstallID, request.Body.Scope,
	)
	if err != nil {
		return nil, err
	}
	observation, err := githubReviewObservationInput(openapi.GitHubReviewObservation{
		ReviewId: request.Body.Observation.ReviewId, CommitId: request.Body.Observation.CommitId,
		CreatingToolCallId: &request.Body.Observation.CreatingToolCallId,
	})
	if err != nil {
		return nil, err
	}
	input := executionstore.RecordGitHubReviewIdentityInput{
		Scope: scope, Observation: observation, Evidence: executionstore.GitHubReviewIdentityEvidence(request.Body.Evidence),
	}
	result, err := s.server.store.Execution().RecordGitHubReviewIdentity(ctx, input)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	return openapi.RecordChannelConnectorGitHubReview200JSONResponse{
		Recorded: result.Recorded, Continue: result.Continue,
	}, nil
}

func githubReviewConnectorScope(
	ctx context.Context,
	appID, installID string,
	body openapi.GitHubReviewOperationScope,
) (executionstore.GitHubReviewOperationScope, error) {
	authority, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return executionstore.GitHubReviewOperationScope{}, err
	}
	input := executionstore.GitHubReviewOperationScope{Capabilities: authority.Capabilities}
	for _, field := range []struct {
		kind  publicid.Kind
		value string
		out   *uuid.UUID
	}{
		{publicid.KindIntegrationApp, appID, &input.IntegrationAppID},
		{publicid.KindIntegrationInstall, installID, &input.IntegrationInstallID},
		{publicid.KindAgent, body.AgentId, &input.AgentID},
		{publicid.KindIntegrationTarget, body.ChannelId, &input.ChannelID},
		{publicid.KindToolCall, body.RequestId, &input.RequestID},
	} {
		id, ok := parseOpenAPIPublicID(field.kind, field.value)
		if !ok {
			return executionstore.GitHubReviewOperationScope{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		*field.out = id
	}
	return input, nil
}

func githubReviewObservationInput(
	body openapi.GitHubReviewObservation,
) (executionstore.GitHubReviewObservation, error) {
	observation := executionstore.GitHubReviewObservation{ReviewID: body.ReviewId}
	if body.CommitId != nil {
		observation.CommitID = *body.CommitId
	}
	if body.CreatingToolCallId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindToolCall, *body.CreatingToolCallId)
		if !ok {
			return observation, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid creating tool call ID")
		}
		observation.CreatingToolCallID = id
	}
	return observation, nil
}
