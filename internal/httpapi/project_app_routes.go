package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) CreateProjectApp(
	ctx context.Context,
	request openapi.CreateProjectAppRequestObject,
) (openapi.CreateProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input, err := parseProjectAppRequest(request.Body, scope.project.OrgID, scope.project.ID)
	if err != nil {
		return nil, err
	}
	app, err := s.server.store.Integrations().CreateProjectApp(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectAppResponse(app)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectApp201JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateProjectApp(
	ctx context.Context,
	request openapi.UpdateProjectAppRequestObject,
) (openapi.UpdateProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	input, err := parseProjectAppRequest(request.Body, scope.project.OrgID, scope.project.ID)
	if err != nil {
		return nil, err
	}
	app, err := s.server.store.Integrations().UpdateProjectApp(ctx, id, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectAppResponse(app)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateProjectApp200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetProjectApp(
	ctx context.Context,
	request openapi.GetProjectAppRequestObject,
) (openapi.GetProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	app, err := s.server.store.Integrations().GetProjectApp(ctx, scope.project.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectAppResponse(app)
	if err != nil {
		return nil, err
	}
	return openapi.GetProjectApp200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteProjectApp(
	ctx context.Context,
	request openapi.DeleteProjectAppRequestObject,
) (openapi.DeleteProjectAppResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	if err := s.server.store.Integrations().DeleteProjectApp(ctx, scope.project.OrgID, scope.project.ID, id); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteProjectApp204Response{}, nil
}

func (s strictOpenAPIServer) ListProjectApps(
	ctx context.Context,
	request openapi.ListProjectAppsRequestObject,
) (openapi.ListProjectAppsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(request.Params.Limit, request.Params.Cursor, publicid.KindProjectApp)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListProjectApps(ctx, integrationstore.ListProjectAppsInput{
		ProjectID: scope.project.ID, Limit: limit, After: after,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.ProjectApp, 0, len(page.Apps))
	for _, app := range page.Apps {
		response, err := projectAppResponse(app)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeNextCursor(page.HasMore, page.Next.CreatedAt, publicid.KindProjectApp, page.Next.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectApps200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func parseProjectAppRequest(
	body *openapi.SaveProjectAppRequest,
	orgID,
	projectID uuid.UUID,
) (integrationstore.SaveProjectAppInput, error) {
	if body == nil {
		return integrationstore.SaveProjectAppInput{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"request body is required",
		)
	}
	input := integrationstore.SaveProjectAppInput{
		OrgID: orgID, ProjectID: projectID, Name: body.Name, AppType: appdefinition.Type(body.AppType),
	}
	if source := body.Settings.Launcher; source != nil {
		launcher := &integrationstore.AppLauncher{
			Trigger:   source.Trigger,
			ScopeKind: source.ScopeKind,
			ScopeRef:  source.ScopeRef,
		}
		for _, sourceSlot := range source.Slots {
			slot := integrationstore.AppLaunchSlot{Key: sourceSlot.Key}
			if sourceSlot.AgentProfileId != nil {
				id, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, *sourceSlot.AgentProfileId)
				if !ok {
					return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid launch slot agent_profile_id")
				}
				slot.AgentProfileID = &id
			}
			if sourceSlot.AgentId != nil {
				id, ok := parseOpenAPIPublicID(publicid.KindAgent, *sourceSlot.AgentId)
				if !ok {
					return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid launch slot agent_id")
				}
				slot.AgentID = &id
			}
			launcher.Slots = append(launcher.Slots, slot)
		}
		input.Settings.Launcher = launcher
	}
	return input, nil
}

func projectAppResponse(app integrationstore.ProjectAppRecord) (openapi.ProjectApp, error) {
	id, err := publicID(publicid.KindProjectApp, app.ID)
	if err != nil {
		return openapi.ProjectApp{}, err
	}
	projectID, err := publicID(publicid.KindProject, app.ProjectID)
	if err != nil {
		return openapi.ProjectApp{}, err
	}
	response := openapi.ProjectApp{
		Id: id, ProjectId: projectID, Name: app.Name, AppType: openapi.AppType(app.AppType),
		State:         openapi.ProjectAppState(app.State),
		SetupRevision: app.SetupRevision, ProviderTenantId: app.ProviderTenantID,
		ProviderAccountRef: app.ProviderAccountRef, ProviderAgentDisplayName: app.ProviderAgentDisplayName,
		CreatedAt: app.CreatedAt, UpdatedAt: app.UpdatedAt,
	}
	response.CredentialSecretId, err = idOrNil(publicid.KindSecret, app.CredentialSecretID)
	if err != nil {
		return openapi.ProjectApp{}, err
	}
	if err := json.Unmarshal(app.ProviderConfig, &response.ProviderConfig); err != nil {
		return openapi.ProjectApp{}, err
	}
	response.LastOauthFlowId, err = idOrNil(publicid.KindIntegrationOAuthFlow, app.LastOAuthFlowID)
	if err != nil {
		return openapi.ProjectApp{}, err
	}
	response.Capabilities, err = appCapabilitiesResponse(app.AppType)
	if err != nil {
		return openapi.ProjectApp{}, err
	}
	if source := app.Settings.Launcher; source != nil {
		launcher := &openapi.AppLauncher{
			Trigger:   source.Trigger,
			ScopeKind: source.ScopeKind,
			ScopeRef:  source.ScopeRef,
			Slots:     make([]openapi.AppLaunchSlot, 0, len(source.Slots)),
		}
		for _, sourceSlot := range source.Slots {
			slot := openapi.AppLaunchSlot{Key: sourceSlot.Key}
			if sourceSlot.AgentProfileID != nil {
				id, err := publicID(publicid.KindAgentProfile, *sourceSlot.AgentProfileID)
				if err != nil {
					return openapi.ProjectApp{}, err
				}
				slot.AgentProfileId = &id
			}
			if sourceSlot.AgentID != nil {
				id, err := publicID(publicid.KindAgent, *sourceSlot.AgentID)
				if err != nil {
					return openapi.ProjectApp{}, err
				}
				slot.AgentId = &id
			}
			launcher.Slots = append(launcher.Slots, slot)
		}
		response.Settings.Launcher = launcher
	}
	return response, nil
}
