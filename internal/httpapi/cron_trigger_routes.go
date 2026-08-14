package httpapi

import (
	"context"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) CreateCronTrigger(
	ctx context.Context,
	request openapi.CreateCronTriggerRequestObject,
) (openapi.CreateCronTriggerResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createCronTrigger(ctx, request, scope.project)
}

func (s strictOpenAPIServer) createCronTrigger(
	ctx context.Context,
	request openapi.CreateCronTriggerRequestObject,
	project identitystore.ProjectRecord,
) (openapi.CreateCronTriggerResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	target, err := parseCronTriggerTarget(request.Body.Target)
	if err != nil {
		return nil, err
	}
	timezone := "UTC"
	if request.Body.Timezone != nil && strings.TrimSpace(*request.Body.Timezone) != "" {
		timezone = strings.TrimSpace(*request.Body.Timezone)
	}
	enabled := true
	if request.Body.Enabled != nil {
		enabled = *request.Body.Enabled
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	trigger, err := s.server.store.Execution().CreateCronTrigger(ctx, executionstore.CreateCronTriggerInput{
		ProjectID:       project.ID,
		Name:            request.Body.Name,
		Target:          target,
		CronExpression:  request.Body.Cron,
		Timezone:        timezone,
		MessageTemplate: request.Body.MessageTemplate,
		Enabled:         enabled,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := cronTriggerResponseFromRecord(trigger)
	if err != nil {
		return nil, err
	}
	if trigger.Created {
		return openapi.CreateCronTrigger201JSONResponse(response), nil
	}
	return openapi.CreateCronTrigger200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListCronTriggers(
	ctx context.Context,
	request openapi.ListCronTriggersRequestObject,
) (openapi.ListCronTriggersResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listCronTriggers(ctx, request.Params, scope.project)
}

func (s strictOpenAPIServer) listCronTriggers(
	ctx context.Context,
	params openapi.ListCronTriggersParams,
	project identitystore.ProjectRecord,
) (openapi.ListCronTriggersResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := executionstore.CronTriggerListFilters{}
	if params.AgentProfileId != nil {
		profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, *params.AgentProfileId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_profile_id")
		}
		filters.AgentProfileID = profileID
	}
	if params.AgentId != nil {
		agentID, ok := parseOpenAPIPublicID(publicid.KindAgent, *params.AgentId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_id")
		}
		filters.AgentID = agentID
	}
	extra := struct{ AgentProfileID, AgentID string }{}
	if filters.AgentProfileID != storage.NilID {
		extra.AgentProfileID = filters.AgentProfileID.String()
	}
	if filters.AgentID != storage.NilID {
		extra.AgentID = filters.AgentID.String()
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: optionalString(params.Sort),
		Cursor: params.Cursor, ListKind: "cron_triggers",
		Scope: project.OrgID.String() + "/" + project.ID.String(), IDKind: publicid.KindCronTrigger,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListCronTriggersForProject(
		ctx,
		executionstore.ListCronTriggersForProjectInput{
			ProjectID: project.ID, Filters: filters, List: list, Limit: limit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.CronTrigger, 0, len(page.Triggers))
	for _, trigger := range page.Triggers {
		response, err := cronTriggerResponseFromRecord(trigger)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "cron_triggers",
		project.OrgID.String()+"/"+project.ID.String(), publicid.KindCronTrigger, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListCronTriggers200JSONResponse(openapi.ListCronTriggersResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) GetCronTrigger(
	ctx context.Context,
	request openapi.GetCronTriggerRequestObject,
) (openapi.GetCronTriggerResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getCronTrigger(ctx, request, scope.project)
}

func (s strictOpenAPIServer) getCronTrigger(
	ctx context.Context,
	request openapi.GetCronTriggerRequestObject,
	project identitystore.ProjectRecord,
) (openapi.GetCronTriggerResponseObject, error) {
	triggerID, ok := parseOpenAPIPublicID(publicid.KindCronTrigger, request.CronTriggerID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	trigger, err := s.server.store.Execution().GetCronTrigger(ctx, project.ID, triggerID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := cronTriggerResponseFromRecord(trigger)
	if err != nil {
		return nil, err
	}
	return openapi.GetCronTrigger200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateCronTrigger(
	ctx context.Context,
	request openapi.UpdateCronTriggerRequestObject,
) (openapi.UpdateCronTriggerResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.updateCronTrigger(ctx, request, scope.project)
}

func (s strictOpenAPIServer) updateCronTrigger(
	ctx context.Context,
	request openapi.UpdateCronTriggerRequestObject,
	project identitystore.ProjectRecord,
) (openapi.UpdateCronTriggerResponseObject, error) {
	triggerID, ok := parseOpenAPIPublicID(publicid.KindCronTrigger, request.CronTriggerID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	timezone := request.Body.Timezone
	if timezone != nil {
		trimmed := strings.TrimSpace(*timezone)
		timezone = &trimmed
	}
	trigger, err := s.server.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID:       project.ID,
		TriggerID:       triggerID,
		Name:            request.Body.Name,
		CronExpression:  request.Body.Cron,
		Timezone:        timezone,
		MessageTemplate: request.Body.MessageTemplate,
		Enabled:         request.Body.Enabled,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := cronTriggerResponseFromRecord(trigger)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateCronTrigger200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteCronTrigger(
	ctx context.Context,
	request openapi.DeleteCronTriggerRequestObject,
) (openapi.DeleteCronTriggerResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.deleteCronTrigger(ctx, request, scope.project)
}

func (s strictOpenAPIServer) deleteCronTrigger(
	ctx context.Context,
	request openapi.DeleteCronTriggerRequestObject,
	project identitystore.ProjectRecord,
) (openapi.DeleteCronTriggerResponseObject, error) {
	triggerID, ok := parseOpenAPIPublicID(publicid.KindCronTrigger, request.CronTriggerID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Execution().DeleteCronTrigger(ctx, project.ID, triggerID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteCronTrigger204Response{}, nil
}

func parseCronTriggerTarget(input openapi.CronTriggerTarget) (executionstore.CronTriggerTarget, error) {
	kind, err := input.Discriminator()
	if err != nil {
		return executionstore.CronTriggerTarget{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"invalid target",
		)
	}
	switch kind {
	case string(executionstore.CronTriggerTargetAgent):
		target, err := input.AsAgentCronTriggerTarget()
		if err != nil {
			return executionstore.CronTriggerTarget{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid target",
			)
		}
		agentID, ok := parseOpenAPIPublicID(publicid.KindAgent, target.AgentId)
		if !ok {
			return executionstore.CronTriggerTarget{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid target agent_id",
			)
		}
		return executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAgent,
			ID:   agentID,
		}, nil
	case string(executionstore.CronTriggerTargetAgentProfile):
		target, err := input.AsAgentProfileCronTriggerTarget()
		if err != nil {
			return executionstore.CronTriggerTarget{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid target",
			)
		}
		profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, target.AgentProfileId)
		if !ok {
			return executionstore.CronTriggerTarget{}, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid target agent_profile_id",
			)
		}
		return executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAgentProfile,
			ID:   profileID,
		}, nil
	default:
		return executionstore.CronTriggerTarget{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"invalid target type",
		)
	}
}

func cronTriggerTargetResponse(target executionstore.CronTriggerTarget) (openapi.CronTriggerTarget, error) {
	var response openapi.CronTriggerTarget
	switch target.Kind {
	case executionstore.CronTriggerTargetAgent:
		agentID, err := publicID(publicid.KindAgent, target.ID)
		if err != nil {
			return openapi.CronTriggerTarget{}, err
		}
		if err := response.FromAgentCronTriggerTarget(openapi.AgentCronTriggerTarget{
			Type:    openapi.AgentCronTriggerTargetTypeAgent,
			AgentId: agentID,
		}); err != nil {
			return openapi.CronTriggerTarget{}, err
		}
	case executionstore.CronTriggerTargetAgentProfile:
		profileID, err := publicID(publicid.KindAgentProfile, target.ID)
		if err != nil {
			return openapi.CronTriggerTarget{}, err
		}
		if err := response.FromAgentProfileCronTriggerTarget(openapi.AgentProfileCronTriggerTarget{
			Type:           openapi.Profile,
			AgentProfileId: profileID,
		}); err != nil {
			return openapi.CronTriggerTarget{}, err
		}
	default:
		return openapi.CronTriggerTarget{}, apierror.FromCode(
			openapi.ErrorCodeInternalError,
			"unsupported cron trigger target kind",
		)
	}
	return response, nil
}

func cronTriggerResponseFromRecord(
	record executionstore.CronTriggerRecord,
) (openapi.CronTrigger, error) {
	id, err := publicID(publicid.KindCronTrigger, record.ID)
	if err != nil {
		return openapi.CronTrigger{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.CronTrigger{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.CronTrigger{}, err
	}
	target, err := cronTriggerTargetResponse(record.Target)
	if err != nil {
		return openapi.CronTrigger{}, err
	}
	return openapi.CronTrigger{
		Id:              id,
		OrgId:           orgID,
		ProjectId:       projectID,
		Name:            record.Name,
		Target:          target,
		Cron:            record.CronExpression,
		Timezone:        record.Timezone,
		MessageTemplate: record.MessageTemplate,
		Enabled:         record.Enabled,
		LastFiredAt:     nullableFromPtr(record.LastFiredAt),
		NextFireAt:      nullableFromPtr(record.NextFireAfter),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}, nil
}
