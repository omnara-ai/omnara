package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) ListToolCalls(
	ctx context.Context,
	request openapi.ListToolCallsRequestObject,
) (openapi.ListToolCallsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindToolCall,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	var state executionstore.ToolCallState
	if request.Params.State != nil {
		state = executionstore.ToolCallState(*request.Params.State)
	}
	toolType := ""
	if request.Params.Type != nil {
		toolType = string(*request.Params.Type)
	}
	page, err := s.server.store.Execution().ListToolCalls(ctx, executionstore.ListToolCallsInput{
		ProjectID: scope.project.ID,
		AgentID:   scope.agent.ID,
		State:     state,
		Type:      toolType,
		Limit:     limit,
		After:     after,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.ToolCall, 0, len(page.ToolCalls))
	var last executionstore.ToolCallRecord
	for _, toolCall := range page.ToolCalls {
		response, err := publicToolCallFromRecord(toolCall)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
		last = toolCall
	}
	nextCursor, err := encodeNextCursor(
		page.HasMore,
		last.CreatedAt,
		publicid.KindToolCall,
		last.ID,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListToolCalls200JSONResponse(openapi.ListToolCallsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) SubmitToolCallResult(
	ctx context.Context,
	request openapi.SubmitToolCallResultRequestObject,
) (openapi.SubmitToolCallResultResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	toolCallID, ok := parseOpenAPIPublicID(publicid.KindToolCall, request.ToolCallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	existing, err := s.server.store.Execution().GetToolCall(
		ctx,
		scope.project.ID,
		scope.agent.ID,
		toolCallID,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if existing.Type != toolcatalog.ToolTypeCustom ||
		existing.State != executionstore.ToolCallStateReady {
		return nil, apierror.FromCode(openapi.ErrorCodeConflict, "custom tool call is not ready for a result")
	}
	outcome := executionstore.ToolResultOutcome(request.Body.Outcome)
	switch outcome {
	case executionstore.ToolResultOutcomeSucceeded, executionstore.ToolResultOutcomeFailed:
	default:
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"custom tool result outcome must be succeeded or failed",
		)
	}
	contentBlocks, err := rawJSONFromContentBlocks(request.Body.ContentBlocks)
	if err != nil {
		return nil, err
	}
	contentBlocks, err = s.server.extractInlineMedia(ctx, mediaIngestContext{
		ProjectID:      scope.project.ID,
		AgentID:        scope.agent.ID,
		IdempotencyKey: "tool-result:" + toolCallID.String(),
	}, contentBlocks)
	if err != nil {
		return nil, mediaIngestAPIError(err)
	}
	result, err := s.server.store.Execution().CompleteCustomToolCall(
		ctx,
		executionstore.CompleteCustomToolCallInput{
			ProjectID:     scope.project.ID,
			AgentID:       scope.agent.ID,
			ID:            toolCallID,
			Outcome:       outcome,
			ContentBlocks: contentBlocks,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	toolCall, err := publicToolCallFromRecord(result.ToolCall)
	if err != nil {
		return nil, err
	}
	toolResult, err := publicToolResultFromRecord(
		result.ToolCall,
		result.Event,
		result.ContentBlocks,
	)
	if err != nil {
		return nil, err
	}
	response := openapi.SubmitToolCallResultResponse{
		ToolCall:   toolCall,
		ToolResult: toolResult,
	}
	return openapi.SubmitToolCallResult201JSONResponse(response), nil
}
