package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

func (s strictOpenAPIServer) ListBudgets(
	ctx context.Context,
	request openapi.ListBudgetsRequestObject,
) (openapi.ListBudgetsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := identitystore.ListBudgetsInput{OrgID: org.ID, Limit: limit}
	params := request.Params
	if params.ScopeType != nil {
		ids, err := parseBudgetScopeIDs(*params.ScopeType, params.ProjectId, params.AgentProfileId, params.AgentId)
		if err != nil {
			return nil, err
		}
		input.FilterScope = true
		input.ProjectID, input.AgentProfileID, input.AgentID = ids.ProjectID, ids.AgentProfileID, ids.AgentID
	} else if params.ProjectId != nil || params.AgentProfileId != nil || params.AgentId != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"project_id, agent_profile_id, and agent_id require scope_type")
	}
	filter := struct {
		Set            bool
		ProjectID      *uuid.UUID
		AgentProfileID *uuid.UUID
		AgentID        *uuid.UUID
	}{input.FilterScope, input.ProjectID, input.AgentProfileID, input.AgentID}
	sort := "created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Cursor: request.Params.Cursor, Sort: &sort, AllowedSorts: sortSet(sort),
		ListKind: "budgets", Scope: org.ID.String(), IDKind: publicid.KindBudget, Extra: filter,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if options.After.Set {
		createdAt, err := listing.ParseTimestampKey(options.After.Key)
		if err != nil || createdAt.IsZero() || options.After.IsNull {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, errMalformedCursor.Error())
		}
		input.After = listing.KeysetCursor{Set: true, CreatedAt: createdAt, ID: options.After.ID}
	}
	page, err := s.server.store.Identity().ListBudgets(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response := openapi.ListBudgetsResponse{Data: make([]openapi.Budget, 0, len(page.Budgets))}
	var last identitystore.BudgetRecord
	for _, record := range page.Budgets {
		budget, err := budgetResponse(record)
		if err != nil {
			return nil, err
		}
		response.Data = append(response.Data, budget)
		last = record
	}
	next, err := encodeResourceListNextCursor(page.HasMore,
		listing.Cursor{Set: last.ID != uuid.Nil, Key: listing.TimestampKey(last.CreatedAt), ID: last.ID},
		options, "budgets", org.ID.String(), publicid.KindBudget, filter)
	if err != nil {
		return nil, err
	}
	response.NextCursor = nullableFromPtr(next)
	return openapi.ListBudgets200JSONResponse(response), nil
}

func (s strictOpenAPIServer) CreateBudget(
	ctx context.Context,
	request openapi.CreateBudgetRequestObject,
) (openapi.CreateBudgetResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := parseBudgetScope(request.Body.Scope)
	if err != nil {
		return nil, err
	}
	amount, err := budgetAmountFromRequest(request.Body.AmountUsd)
	if err != nil {
		return nil, err
	}
	record, err := s.server.store.Identity().CreateBudget(ctx, identitystore.CreateBudgetInput{
		OrgID: org.ID, ProjectID: ids.ProjectID, AgentProfileID: ids.AgentProfileID, AgentID: ids.AgentID,
		Interval: string(request.Body.Interval), AmountUSD: amount,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := budgetResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateBudget201JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetBudget(
	ctx context.Context,
	request openapi.GetBudgetRequestObject,
) (openapi.GetBudgetResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindBudget, request.BudgetID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Identity().GetBudget(ctx, org.ID, id)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := budgetResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetBudget200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateBudget(
	ctx context.Context,
	request openapi.UpdateBudgetRequestObject,
) (openapi.UpdateBudgetResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindBudget, request.BudgetID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	amount, err := budgetAmountFromRequest(request.Body.AmountUsd)
	if err != nil {
		return nil, err
	}
	record, err := s.server.store.Identity().UpdateBudgetAmount(ctx, org.ID, id, amount)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := budgetResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateBudget200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteBudget(
	ctx context.Context,
	request openapi.DeleteBudgetRequestObject,
) (openapi.DeleteBudgetResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindBudget, request.BudgetID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Identity().DeleteBudget(ctx, org.ID, id); err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.DeleteBudget204Response{}, nil
}

type budgetScopeIDs struct {
	ProjectID      *uuid.UUID
	AgentProfileID *uuid.UUID
	AgentID        *uuid.UUID
}

func parseBudgetScope(input openapi.BudgetScope) (budgetScopeIDs, error) {
	invalid := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid budget scope")
	scopeType, err := input.Discriminator()
	if err != nil {
		return budgetScopeIDs{}, invalid
	}
	var projectID, profileID, agentID *string
	switch openapi.BudgetScopeType(scopeType) {
	case openapi.BudgetScopeOrg:
	case openapi.BudgetScopeProject:
		scope, err := input.AsProjectBudgetScope()
		if err != nil {
			return budgetScopeIDs{}, invalid
		}
		projectID = &scope.ProjectId
	case openapi.BudgetScopeProfile:
		scope, err := input.AsAgentProfileBudgetScope()
		if err != nil {
			return budgetScopeIDs{}, invalid
		}
		projectID, profileID = &scope.ProjectId, &scope.AgentProfileId
	case openapi.BudgetScopeAgent:
		scope, err := input.AsAgentBudgetScope()
		if err != nil {
			return budgetScopeIDs{}, invalid
		}
		projectID, agentID = &scope.ProjectId, &scope.AgentId
	}
	return parseBudgetScopeIDs(openapi.BudgetScopeType(scopeType), projectID, profileID, agentID)
}

func parseBudgetScopeIDs(
	scopeType openapi.BudgetScopeType, projectID, profileID, agentID *string,
) (budgetScopeIDs, error) {
	valid := false
	switch scopeType {
	case openapi.BudgetScopeOrg:
		valid = projectID == nil && profileID == nil && agentID == nil
	case openapi.BudgetScopeProject:
		valid = projectID != nil && profileID == nil && agentID == nil
	case openapi.BudgetScopeProfile:
		valid = projectID != nil && profileID != nil && agentID == nil
	case openapi.BudgetScopeAgent:
		valid = projectID != nil && profileID == nil && agentID != nil
	}
	if !valid {
		return budgetScopeIDs{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid budget scope")
	}
	var ids budgetScopeIDs
	for _, field := range []struct {
		raw    *string
		kind   publicid.Kind
		target **uuid.UUID
	}{
		{projectID, publicid.KindProject, &ids.ProjectID},
		{profileID, publicid.KindAgentProfile, &ids.AgentProfileID},
		{agentID, publicid.KindAgent, &ids.AgentID},
	} {
		if field.raw == nil {
			continue
		}
		id, ok := parseOpenAPIPublicID(field.kind, *field.raw)
		if !ok {
			return budgetScopeIDs{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid budget scope id")
		}
		*field.target = &id
	}
	return ids, nil
}

func budgetAmountFromRequest(raw string) (string, error) {
	amount, ok := modelenvelope.ParseProviderReportedCostUSD(raw)
	if !ok || amount == "0" {
		return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, "amount_usd must be a positive decimal")
	}
	return string(amount), nil
}

func budgetResponse(record identitystore.BudgetRecord) (openapi.Budget, error) {
	id, err := publicID(publicid.KindBudget, record.ID)
	if err != nil {
		return openapi.Budget{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.Budget{}, err
	}
	scope, err := budgetScopeResponse(record)
	if err != nil {
		return openapi.Budget{}, err
	}
	response := openapi.Budget{
		Id: id, OrgId: orgID, Scope: scope, Interval: openapi.BudgetInterval(record.Interval),
		AmountUsd: record.AmountUSD, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.Usage != nil {
		response.Usage = &openapi.BudgetUsage{AmountUsd: record.Usage.AmountUSD}
	}
	return response, nil
}

func budgetScopeResponse(record identitystore.BudgetRecord) (openapi.BudgetScope, error) {
	var scope openapi.BudgetScope
	if record.ProjectID == nil {
		err := scope.FromOrgBudgetScope(openapi.OrgBudgetScope{Type: openapi.OrgBudgetScopeTypeOrg})
		return scope, err
	}
	projectID, err := publicID(publicid.KindProject, *record.ProjectID)
	if err != nil {
		return scope, err
	}
	switch {
	case record.AgentProfileID != nil:
		profileID, err := publicID(publicid.KindAgentProfile, *record.AgentProfileID)
		if err != nil {
			return scope, err
		}
		err = scope.FromAgentProfileBudgetScope(openapi.AgentProfileBudgetScope{
			Type: openapi.AgentProfileBudgetScopeTypeProfile, ProjectId: projectID, AgentProfileId: profileID,
		})
		return scope, err
	case record.AgentID != nil:
		agentID, err := publicID(publicid.KindAgent, *record.AgentID)
		if err != nil {
			return scope, err
		}
		err = scope.FromAgentBudgetScope(openapi.AgentBudgetScope{
			Type: openapi.AgentBudgetScopeTypeAgent, ProjectId: projectID, AgentId: agentID,
		})
		return scope, err
	}
	err = scope.FromProjectBudgetScope(openapi.ProjectBudgetScope{
		Type: openapi.ProjectBudgetScopeTypeProject, ProjectId: projectID,
	})
	return scope, err
}
