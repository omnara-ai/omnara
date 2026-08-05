package httpapi

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) CreateProjectMachineGrant(
	ctx context.Context,
	request openapi.CreateProjectMachineGrantRequestObject,
) (openapi.CreateProjectMachineGrantResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, project, scopeErr := s.server.projectScope(
		ctx,
		request.OrgID,
		request.ProjectID,
		identitystore.ProjectActionAccessManage,
	)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	machineID, ok := parseOpenAPIPublicID(publicid.KindMachine, request.Body.MachineId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid machine_id")
	}
	machine, scopeErr := s.server.machineScope(
		ctx,
		request.OrgID,
		request.Body.MachineId,
		executionstore.MachineActionManage,
	)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	return s.createProjectMachineGrant(ctx, request, org, project, machineID, machine)
}

func (s strictOpenAPIServer) createProjectMachineGrant(
	ctx context.Context,
	request openapi.CreateProjectMachineGrantRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
	machineID storage.ID,
	machine executionstore.MachineRecord,
) (openapi.CreateProjectMachineGrantResponseObject, error) {
	if machine.SourceKind != executionstore.MachineSourceKindBYO {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	metadata, err := rawJSONFromPointer(request.Body.Metadata)
	if err != nil {
		return nil, err
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	grant, grantedMachine, err := s.server.store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          org.ID,
			ProjectID:      project.ID,
			MachineID:      machineID,
			Description:    description,
			IdempotencyKey: idempotencyKey,
			Metadata:       metadata,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	grantResponse, err := projectMachineGrantResponse(grant)
	if err != nil {
		return nil, err
	}
	machineResponse, err := machineResponse(grantedMachine)
	if err != nil {
		return nil, err
	}
	response := openapi.CreateProjectMachineGrantResponse{Grant: grantResponse, Machine: machineResponse}
	if grant.Created {
		return openapi.CreateProjectMachineGrant201JSONResponse(response), nil
	}
	return openapi.CreateProjectMachineGrant200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListProjectMachineGrants(
	ctx context.Context,
	request openapi.ListProjectMachineGrantsRequestObject,
) (openapi.ListProjectMachineGrantsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listProjectMachineGrants(ctx, request, scope.org, scope.project)
}

func (s strictOpenAPIServer) listProjectMachineGrants(
	ctx context.Context,
	request openapi.ListProjectMachineGrantsRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
) (openapi.ListProjectMachineGrantsResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	// Pool-derived grants are managed through their parent machine-pool grant.
	// Only explicit grants belong in this list because they are the grants this
	// route's revoke endpoint permits callers to revoke directly.
	filters := executionstore.MachineGrantListFilters{
		SourceKinds: []executionstore.ProjectMachineGrantSourceKind{
			executionstore.ProjectMachineGrantSourceKindExplicit,
		},
	}
	scopeKey := org.ID.String() + "/" + project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_machine_grants",
		Scope: scopeKey, IDKind: publicid.KindProjectMachineGrant,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListProjectMachineGrants(
		ctx,
		executionstore.ListProjectMachineGrantsInput{
			OrgID: org.ID, ProjectID: project.ID,
			Filters: filters, Limit: limit, List: list,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := make([]openapi.ProjectMachineGrantListItem, 0, len(page.Grants))
	for _, record := range page.Grants {
		response, err := projectMachineGrantResponse(record.Grant)
		if err != nil {
			return nil, err
		}
		machine, err := machineSummaryResponse(record.Machine)
		if err != nil {
			return nil, err
		}
		out = append(out, openapi.ProjectMachineGrantListItem{Grant: response, Machine: machine})
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "project_machine_grants",
		scopeKey, publicid.KindProjectMachineGrant, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectMachineGrants200JSONResponse(
		openapi.ListProjectMachineGrantsResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) ListVisibleProjectMachines(
	ctx context.Context,
	request openapi.ListVisibleProjectMachinesRequestObject,
) (openapi.ListVisibleProjectMachinesResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listVisibleProjectMachines(ctx, request, scope.org, scope.project)
}

func (s strictOpenAPIServer) listVisibleProjectMachines(
	ctx context.Context,
	request openapi.ListVisibleProjectMachinesRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
) (openapi.ListVisibleProjectMachinesResponseObject, error) {
	principal, _ := principalFromContext(ctx)
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := executionstore.MachineListFilters{}
	if request.Params.SourceKind != nil {
		filters.SourceKinds = []executionstore.MachineSourceKind{
			executionstore.MachineSourceKind(*request.Params.SourceKind),
		}
	}
	extra := struct {
		SourceKinds []executionstore.MachineSourceKind
	}{filters.SourceKinds}
	scopeKey := org.ID.String() + "/" + project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_machines",
		Scope: scopeKey, IDKind: publicid.KindMachine,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{
			OrgID:     org.ID,
			ProjectID: project.ID,
			Principal: principal,
			Filters:   filters, Limit: limit, List: list,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := make([]openapi.VisibleMachine, 0, len(page.Machines))
	for _, record := range page.Machines {
		response, err := projectVisibleMachineResponse(project.ID, record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "project_machines", scopeKey, publicid.KindMachine, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListVisibleProjectMachines200JSONResponse(
		openapi.ListVisibleMachinesResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) DeleteProjectMachineGrant(
	ctx context.Context,
	request openapi.DeleteProjectMachineGrantRequestObject,
) (openapi.DeleteProjectMachineGrantResponseObject, error) {
	org, project, scopeErr := s.server.projectScope(
		ctx,
		request.OrgID,
		request.ProjectID,
		identitystore.ProjectActionAccessManage,
	)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	grantID, ok := parseOpenAPIPublicID(publicid.KindProjectMachineGrant, request.GrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	grant, err := s.server.store.Execution().GetProjectMachineGrant(ctx, org.ID, project.ID, grantID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if grant.SourceKind != executionstore.ProjectMachineGrantSourceKindExplicit {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	machineID, err := publicID(publicid.KindMachine, grant.MachineID)
	if err != nil {
		return nil, err
	}
	if _, scopeErr := s.server.machineScope(
		ctx,
		request.OrgID,
		machineID,
		executionstore.MachineActionManage,
	); scopeErr != nil {
		return nil, *scopeErr
	}
	return s.deleteProjectMachineGrant(ctx, org, project, grantID)
}

func (s strictOpenAPIServer) deleteProjectMachineGrant(
	ctx context.Context,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
	grantID storage.ID,
) (openapi.DeleteProjectMachineGrantResponseObject, error) {
	if _, err := s.server.store.Execution().DeleteProjectMachineGrant(ctx, org.ID, project.ID, grantID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteProjectMachineGrant204Response{}, nil
}

func projectMachineGrantResponse(record executionstore.ProjectMachineGrantRecord) (openapi.ProjectMachineGrant, error) {
	id, err := publicID(publicid.KindProjectMachineGrant, record.ID)
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	machineID, err := publicID(publicid.KindMachine, record.MachineID)
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	poolGrantID, err := idOrNil(publicid.KindProjectMachinePoolGrant, record.ProjectMachinePoolGrantID)
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	metadata, err := jsonMapOrFallback(record.Metadata, json.RawMessage(`{}`))
	if err != nil {
		return openapi.ProjectMachineGrant{}, err
	}
	return openapi.ProjectMachineGrant{
		Id:                        id,
		OrgId:                     orgID,
		ProjectId:                 projectID,
		MachineId:                 machineID,
		SourceKind:                openapi.ProjectMachineGrantSourceKind(record.SourceKind),
		ProjectMachinePoolGrantId: nullableFromPtr(poolGrantID),
		Description:               record.Description,
		Metadata:                  metadata,
		CreatedAt:                 record.CreatedAt,
		UpdatedAt:                 record.UpdatedAt,
	}, nil
}
