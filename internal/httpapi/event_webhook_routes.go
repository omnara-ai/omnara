package httpapi

import (
	"context"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func parseEventWebhookScope(input openapi.EventWebhookScope) (executionstore.EventWebhookScope, error) {
	kind, err := input.Discriminator()
	if err != nil {
		return executionstore.EventWebhookScope{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid scope")
	}
	var profileID, agentID *string
	switch executionstore.EventWebhookScopeKind(kind) {
	case executionstore.EventWebhookScopeProject:
	case executionstore.EventWebhookScopeAgentProfile:
		scope, err := input.AsAgentProfileEventWebhookScope()
		if err != nil {
			return executionstore.EventWebhookScope{}, err
		}
		profileID = &scope.AgentProfileId
	case executionstore.EventWebhookScopeAgent:
		scope, err := input.AsAgentEventWebhookScope()
		if err != nil {
			return executionstore.EventWebhookScope{}, err
		}
		agentID = &scope.AgentId
	}
	return parseEventWebhookScopeIDs(executionstore.EventWebhookScopeKind(kind), profileID, agentID)
}

func parseEventWebhookScopeIDs(
	kind executionstore.EventWebhookScopeKind, profileID, agentID *string,
) (executionstore.EventWebhookScope, error) {
	scope := executionstore.EventWebhookScope{Kind: kind}
	var rawID *string
	var idKind publicid.Kind
	switch {
	case kind == executionstore.EventWebhookScopeProject && profileID == nil && agentID == nil:
		return scope, nil
	case kind == executionstore.EventWebhookScopeAgentProfile && profileID != nil && agentID == nil:
		rawID, idKind = profileID, publicid.KindAgentProfile
	case kind == executionstore.EventWebhookScopeAgent && agentID != nil && profileID == nil:
		rawID, idKind = agentID, publicid.KindAgent
	default:
		return scope, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid webhook scope")
	}
	id, ok := parseOpenAPIPublicID(idKind, *rawID)
	if !ok {
		return scope, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid webhook scope id")
	}
	scope.ID = id
	return scope, nil
}

func eventWebhookScopeResponse(scope executionstore.EventWebhookScope) (openapi.EventWebhookScope, error) {
	var response openapi.EventWebhookScope
	switch scope.Kind {
	case executionstore.EventWebhookScopeProject:
		err := response.FromProjectEventWebhookScope(openapi.ProjectEventWebhookScope{
			Kind: openapi.ProjectEventWebhookScopeKindProject,
		})
		return response, err
	case executionstore.EventWebhookScopeAgentProfile:
		id, err := publicID(publicid.KindAgentProfile, scope.ID)
		if err != nil {
			return response, err
		}
		err = response.FromAgentProfileEventWebhookScope(openapi.AgentProfileEventWebhookScope{
			Kind: openapi.AgentProfileEventWebhookScopeKindProfile, AgentProfileId: id,
		})
		return response, err
	case executionstore.EventWebhookScopeAgent:
		id, err := publicID(publicid.KindAgent, scope.ID)
		if err != nil {
			return response, err
		}
		err = response.FromAgentEventWebhookScope(openapi.AgentEventWebhookScope{
			Kind: openapi.AgentEventWebhookScopeKindAgent, AgentId: id,
		})
		return response, err
	default:
		return response, apierror.FromCode(openapi.ErrorCodeInternalError, "unsupported webhook scope")
	}
}

func eventWebhookEventTypes(eventTypes []openapi.EventWebhookEventType) []string {
	result := make([]string, len(eventTypes))
	for i, eventType := range eventTypes {
		result[i] = string(eventType)
	}
	return result
}

func eventWebhookResponseFromRecord(record executionstore.EventWebhookRecord) (openapi.EventWebhook, error) {
	id, err := publicID(publicid.KindEventWebhook, record.ID)
	if err != nil {
		return openapi.EventWebhook{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.EventWebhook{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.EventWebhook{}, err
	}
	scope, err := eventWebhookScopeResponse(record.Scope)
	if err != nil {
		return openapi.EventWebhook{}, err
	}
	result := openapi.EventWebhook{Id: id, OrgId: orgID, ProjectId: projectID, Scope: scope,
		Url: record.Settings.URL, EventTypes: make([]openapi.EventWebhookEventType, len(record.Settings.EventTypes)),
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	for i, eventType := range record.Settings.EventTypes {
		result.EventTypes[i] = openapi.EventWebhookEventType(eventType)
	}
	if record.Settings.SigningSecretID != uuid.Nil {
		value, err := publicID(publicid.KindSecret, record.Settings.SigningSecretID)
		if err != nil {
			return openapi.EventWebhook{}, err
		}
		result.SigningSecretId = &value
	}
	return result, nil
}

func (s strictOpenAPIServer) CreateEventWebhook(
	ctx context.Context,
	request openapi.CreateEventWebhookRequestObject,
) (openapi.CreateEventWebhookResponseObject, error) {
	projectScope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := parseEventWebhookScope(request.Body.Scope)
	if err != nil {
		return nil, err
	}
	settings := webhookconfig.Settings{URL: request.Body.Url, EventTypes: eventWebhookEventTypes(request.Body.EventTypes)}
	if request.Body.SigningSecretId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindSecret, *request.Body.SigningSecretId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid signing_secret_id")
		}
		settings.SigningSecretID = id
	}
	record, err := s.server.store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
		ProjectID: projectScope.project.ID, Scope: scope, Settings: settings,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := eventWebhookResponseFromRecord(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateEventWebhook201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListEventWebhooks(
	ctx context.Context,
	request openapi.ListEventWebhooksRequestObject,
) (openapi.ListEventWebhooksResponseObject, error) {
	projectScope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := parseEventWebhookScopeIDs(executionstore.EventWebhookScopeKind(request.Params.ScopeKind),
		request.Params.AgentProfileId, request.Params.AgentId)
	if err != nil {
		return nil, err
	}
	records, err := s.server.store.Execution().ListEventWebhooks(ctx, projectScope.project.ID, scope)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response := openapi.ListEventWebhooksResponse{Data: make([]openapi.EventWebhook, 0, len(records))}
	for _, record := range records {
		webhook, err := eventWebhookResponseFromRecord(record)
		if err != nil {
			return nil, err
		}
		response.Data = append(response.Data, webhook)
	}
	return openapi.ListEventWebhooks200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateEventWebhook(
	ctx context.Context,
	request openapi.UpdateEventWebhookRequestObject,
) (openapi.UpdateEventWebhookResponseObject, error) {
	projectScope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindEventWebhook, request.WebhookID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	input := executionstore.UpdateEventWebhookInput{
		ProjectID: projectScope.project.ID, WebhookID: id, URL: request.Body.Url,
	}
	if request.Body.EventTypes != nil {
		input.EventTypes = eventWebhookEventTypes(*request.Body.EventTypes)
	}
	if request.Body.SigningSecretId.IsSpecified() {
		secretID := uuid.Nil
		if !request.Body.SigningSecretId.IsNull() {
			rawID, err := request.Body.SigningSecretId.Get()
			if err != nil {
				return nil, err
			}
			var ok bool
			secretID, ok = parseOpenAPIPublicID(publicid.KindSecret, rawID)
			if !ok {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid signing_secret_id")
			}
		}
		input.SigningSecretID = &secretID
	}
	record, err := s.server.store.Execution().UpdateEventWebhook(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := eventWebhookResponseFromRecord(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateEventWebhook200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteEventWebhook(
	ctx context.Context,
	request openapi.DeleteEventWebhookRequestObject,
) (openapi.DeleteEventWebhookResponseObject, error) {
	projectScope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindEventWebhook, request.WebhookID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Execution().DeleteEventWebhook(ctx, projectScope.project.ID, id); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteEventWebhook204Response{}, nil
}
