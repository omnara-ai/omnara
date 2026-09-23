package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func (s strictOpenAPIServer) ListAgentInteractions(
	ctx context.Context,
	request openapi.ListAgentInteractionsRequestObject,
) (openapi.ListAgentInteractionsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listAgentInteractions(ctx, request.Params, scope.project.OrgID, scope.project.ID, scope.agent.ID)
}

func (s strictOpenAPIServer) listAgentInteractions(
	ctx context.Context,
	params openapi.ListAgentInteractionsParams,
	orgID, projectID, agentID uuid.UUID,
) (openapi.ListAgentInteractionsResponseObject, error) {
	limit, after, err := parseOpenAPIPageParams(params.Limit, params.Cursor, publicid.KindAgentInteraction)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	var state executionstore.AgentInteractionState
	if params.State != nil {
		state = executionstore.AgentInteractionState(*params.State)
	}
	agentIDs := []uuid.UUID{agentID}
	if params.IncludeSubagents != nil && *params.IncludeSubagents {
		descendants, err := s.server.store.Execution().ListAgentDescendantIDs(ctx, projectID, agentID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		agentIDs = append(agentIDs, descendants...)
	}
	page, err := s.server.store.Execution().ListAgentInteractions(
		ctx,
		executionstore.ListAgentInteractionsInput{
			ProjectID: projectID,
			AgentIDs:  agentIDs,
			State:     state,
			Limit:     limit,
			After:     after,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.AgentInteraction, 0, len(page.Interactions))
	var last executionstore.AgentInteractionRecord
	for _, item := range page.Interactions {
		response, err := agentInteractionResponseFromRecord(orgID, item.AgentInteractionRecord)
		if err != nil {
			return nil, err
		}
		if item.AgentID != agentID {
			response.AgentName = &item.AgentName
			response.SubagentKey = ptrFromNonEmpty(item.SubagentKey)
		}
		data = append(data, response)
		last = item.AgentInteractionRecord
	}
	nextCursor, err := encodeNextCursor(
		page.HasMore,
		last.CreatedAt,
		publicid.KindAgentInteraction,
		last.ID,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListAgentInteractions200JSONResponse(openapi.ListAgentInteractionsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) ResolveAgentInteraction(
	ctx context.Context,
	request openapi.ResolveAgentInteractionRequestObject,
) (openapi.ResolveAgentInteractionResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.resolveAgentInteraction(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) resolveAgentInteraction(
	ctx context.Context,
	request openapi.ResolveAgentInteractionRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ResolveAgentInteractionResponseObject, error) {
	interactionID, ok := parseOpenAPIPublicID(publicid.KindAgentInteraction, request.InteractionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	body := request.Body
	existing, found, err := s.server.store.Execution().GetAgentInteraction(ctx, project.ID, agent.ID, interactionID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if !found {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	resolution, err := publicInteractionResolution(existing, body.Answers)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == uuid.Nil {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	resolvedBy, err := requestActorParams(project, principal, body.Actor)
	if err != nil {
		return nil, err
	}
	record, err := s.server.store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID:  project.ID,
			AgentID:    agent.ID,
			ID:         interactionID,
			Resolution: resolution,
			Actor:      resolvedBy,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := agentInteractionResponseFromRecord(project.OrgID, record)
	if err != nil {
		return nil, err
	}
	s.server.dismissInteractionAsync(ctx, record)
	return openapi.ResolveAgentInteraction200JSONResponse(response), nil
}

func publicInteractionResolution(
	record executionstore.AgentInteractionRecord,
	answers []openapi.InteractionAnswer,
) (interactionform.Resolution, error) {
	resolution := interactionform.Resolution{
		Answers: make([]interactionform.Answer, 0, len(answers)),
	}
	for _, answer := range answers {
		resolution.Answers = append(resolution.Answers, interactionform.Answer{
			OptionIndices: append([]int(nil), answer.OptionIndices...),
			Text:          answer.Text,
		})
	}
	value, err := record.Form()
	if err != nil {
		return interactionform.Resolution{}, err
	}
	normalized, err := interactionform.NormalizeResolution(value, resolution)
	if err != nil {
		return interactionform.Resolution{}, err
	}
	return normalized, nil
}

func agentInteractionResponseFromRecord(
	orgIDValue uuid.UUID,
	record executionstore.AgentInteractionRecord,
) (openapi.AgentInteraction, error) {
	id, err := publicID(publicid.KindAgentInteraction, record.ID)
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, orgIDValue)
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	agentID, err := publicID(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	toolCallID, err := publicID(publicid.KindToolCall, record.ToolCallID)
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	request, err := record.Form()
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	response := openapi.AgentInteraction{
		Id:              id,
		OrgId:           orgID,
		ProjectId:       projectID,
		AgentId:         agentID,
		ToolCallId:      toolCallID,
		InteractionKind: openapi.AgentInteractionKind(record.InteractionKind),
		State:           openapi.AgentInteractionState(record.State),
		Request:         openAPIInteractionForm(request),
		CreatedAt:       record.CreatedAt,
		ResolvedAt:      timePtrFromZero(record.ResolvedAt),
	}
	destination, err := record.CapturedDestination()
	if err != nil {
		return openapi.AgentInteraction{}, err
	}
	if destination != nil {
		appID, err := publicID(publicid.KindProjectApp, destination.AppID)
		if err != nil {
			return openapi.AgentInteraction{}, err
		}
		targetID, err := publicID(publicid.KindAppTarget, destination.AppTargetID)
		if err != nil {
			return openapi.AgentInteraction{}, err
		}
		var args map[string]interface{}
		if err := json.Unmarshal(destination.Args, &args); err != nil {
			return openapi.AgentInteraction{}, err
		}
		response.Destination = &openapi.AgentInteractionDestination{
			AppType:     openapi.AppType(destination.AppType),
			Args:        args,
			HandlerKey:  destination.HandlerKey,
			AppId:       appID,
			AppTargetId: targetID,
			Address: openapi.AppConversationAddress{
				Kind: destination.Address.Kind, Ref: destination.Address.Ref,
			},
		}
	}
	if len(record.PresentationReceipt) != 0 {
		var receipt openapi.InteractionPresentationReceipt
		if err := json.Unmarshal(record.PresentationReceipt, &receipt); err != nil {
			return openapi.AgentInteraction{}, err
		}
		response.PresentationReceipt = &receipt
	}
	if record.InteractionKind == executionstore.AgentInteractionKindPermission {
		permissionRequest, err := toolpermission.ParseRequest(record.Request)
		if err != nil {
			return openapi.AgentInteraction{}, err
		}
		response.ToolName = &permissionRequest.Authorization.ToolName
	}
	if record.State == executionstore.AgentInteractionStateResolved {
		resolution, err := interactionform.ParseResolution(request, record.Resolution)
		if err != nil {
			return openapi.AgentInteraction{}, err
		}
		value := openAPIInteractionResolution(resolution)
		response.Resolution = &value
	}
	if record.ResolvedByInputID != uuid.Nil {
		resolvedByInputID, err := publicID(publicid.KindAgentInput, record.ResolvedByInputID)
		if err != nil {
			return openapi.AgentInteraction{}, err
		}
		response.ResolvedByInputId = &resolvedByInputID
	}
	return response, nil
}

func openAPIInteractionForm(value interactionform.Form) openapi.InteractionForm {
	out := openapi.InteractionForm{
		Title:     value.Title,
		Questions: make([]openapi.InteractionFormQuestion, 0, len(value.Questions)),
	}
	if len(value.Context) > 0 {
		contextItems := make([]openapi.InteractionFormContextItem, 0, len(value.Context))
		for _, item := range value.Context {
			contextItems = append(contextItems, openapi.InteractionFormContextItem{
				Label: item.Label,
				Value: item.Value,
			})
		}
		out.Context = &contextItems
	}
	for _, question := range value.Questions {
		converted := openapi.InteractionFormQuestion{
			Prompt:   question.Prompt,
			Multiple: question.Multiple,
			Options:  make([]openapi.InteractionFormOption, 0, len(question.Options)),
		}
		for _, option := range question.Options {
			converted.Options = append(converted.Options, openapi.InteractionFormOption{
				Label:      option.Label,
				AllowsText: option.AllowsText,
			})
		}
		out.Questions = append(out.Questions, converted)
	}
	return out
}

func openAPIInteractionResolution(
	value interactionform.Resolution,
) openapi.InteractionResolution {
	out := openapi.InteractionResolution{
		Answers: make([]openapi.InteractionAnswer, 0, len(value.Answers)),
	}
	for _, answer := range value.Answers {
		out.Answers = append(out.Answers, openapi.InteractionAnswer{
			OptionIndices: append([]int(nil), answer.OptionIndices...),
			Text:          answer.Text,
		})
	}
	return out
}

func timePtrFromZero(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (s *Server) dismissInteractionAsync(ctx context.Context, record executionstore.AgentInteractionRecord) {
	if len(record.PresentationReceipt) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		presenter := apps.InteractionPresenter{Store: s.store, HTTPClient: s.appHTTPClient, Log: s.log}
		if err := presenter.Dismiss(ctx, record); err != nil {
			s.log.Warn("interaction dismissal failed", "interaction_id", record.ID, "error", err)
		}
	}()
}
