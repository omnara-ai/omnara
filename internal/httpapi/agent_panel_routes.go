package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
)

func agentPanelScopeResponse(scope orglifecycle.AgentPanelScope) (openapi.AgentPanelScope, error) {
	orgID, err := publicID(publicid.KindOrganization, scope.OrgID)
	if err != nil {
		return openapi.AgentPanelScope{}, err
	}
	projectID, err := publicID(publicid.KindProject, scope.ProjectID)
	if err != nil {
		return openapi.AgentPanelScope{}, err
	}
	return openapi.AgentPanelScope{
		OrgId:       orgID,
		ProjectId:   projectID,
		OrgName:     scope.OrgName,
		ProjectName: scope.ProjectName,
	}, nil
}

func (s strictOpenAPIServer) agentPanelResponse(
	ctx context.Context,
	principal identitystore.PrincipalRecord,
) (openapi.AgentPanel, error) {
	record, err := s.server.store.Organizations().GetAgentPanel(ctx, principal)
	if err != nil {
		return openapi.AgentPanel{}, apierror.UserScoped(err)
	}
	response := openapi.AgentPanel{
		Enabled:  record.Enabled,
		Revision: record.Revision,
		State:    openapi.AgentPanelState(record.State),
	}
	if record.Selection != nil {
		scope, err := agentPanelScopeResponse(record.Selection.AgentPanelScope)
		if err != nil {
			return openapi.AgentPanel{}, err
		}
		agentID, err := publicID(publicid.KindAgent, record.Selection.AgentID)
		if err != nil {
			return openapi.AgentPanel{}, err
		}
		response.Selection = &openapi.AgentPanelSelection{
			AgentId:    agentID,
			Name:       record.Selection.Name,
			Scope:      scope,
			CanOperate: record.Selection.CanOperate,
		}
	}
	if record.Setup != nil {
		scope, err := agentPanelScopeResponse(record.Setup.AgentPanelScope)
		if err != nil {
			return openapi.AgentPanel{}, err
		}
		response.Setup = &openapi.AgentPanelSetup{Scope: scope, Ready: record.Setup.Ready, RequestId: record.Setup.ProjectID}
	}
	return response, nil
}

func (s strictOpenAPIServer) GetAgentPanel(
	ctx context.Context,
	_ openapi.GetAgentPanelRequestObject,
) (openapi.GetAgentPanelResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	response, err := s.agentPanelResponse(ctx, principal)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgentPanel200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateAgentPanel(
	ctx context.Context,
	request openapi.UpdateAgentPanelRequestObject,
) (openapi.UpdateAgentPanelResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	input := orglifecycle.UpdateAgentPanelInput{
		Principal:   principal,
		ChangeAgent: request.Body.AgentId.IsSpecified(),
		Enabled:     request.Body.Enabled,
	}
	if input.ChangeAgent && !request.Body.AgentId.IsNull() {
		input.AgentID, ok = parseOpenAPIPublicID(publicid.KindAgent, request.Body.AgentId.MustGet())
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_id")
		}
	}
	if err := s.server.store.Organizations().UpdateAgentPanel(ctx, input); err != nil {
		return nil, apierror.UserScoped(err)
	}
	response, err := s.agentPanelResponse(ctx, principal)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateAgentPanel200JSONResponse(response), nil
}

func (s strictOpenAPIServer) CreateAssistantAgent(
	ctx context.Context,
	request openapi.CreateAssistantAgentRequestObject,
) (openapi.CreateAssistantAgentResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := principalFromContext(ctx)
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	body := request.Body
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, body.ProjectId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid project_id")
	}
	var modelID uuid.UUID
	if body.ModelId != nil {
		modelID, ok = parseOpenAPIPublicID(publicid.KindConfiguredModel, *body.ModelId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid model_id")
		}
	}
	endpoint := s.server.publicAPIURL
	if endpoint == "" {
		endpoint = s.server.publicURL
	}
	origin, err := parseConfiguredOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	if origin.url == "" {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"configure a public API URL before creating an assistant")
	}
	agentID, err := s.server.store.Organizations().CreateAssistantAgent(ctx, orglifecycle.CreateAssistantAgentInput{
		Principal: principal, OrgID: org.ID, ProjectID: projectID, ModelID: modelID,
		RequestID: body.RequestId, ExpectedRevision: body.ExpectedRevision, Automatic: body.Automatic,
		MCPURL: origin.url + apimcp.Path, ReadOnlyTools: s.server.apiMCPReadOnlyTools,
		AgentConfigOptions: s.server.agentConfigOptions,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	publicAgent, err := publicID(publicid.KindAgent, agentID)
	if err != nil {
		return nil, err
	}
	response, err := s.agentPanelResponse(ctx, principal)
	if err != nil {
		return nil, err
	}
	return openapi.CreateAssistantAgent200JSONResponse{AgentId: publicAgent, Panel: response}, nil
}
