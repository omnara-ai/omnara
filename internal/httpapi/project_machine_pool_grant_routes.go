package httpapi

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) CreateProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.CreateProjectMachinePoolGrantRequestObject,
) (openapi.CreateProjectMachinePoolGrantResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !s.server.orgManageAllowed(ctx, scope.org.ID) {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	poolID, ok := parseOpenAPIPublicID(publicid.KindMachinePool, request.Body.MachinePoolId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid machine_pool_id")
	}
	return s.createProjectMachinePoolGrant(ctx, request, scope.org, scope.project, poolID)
}

func (s strictOpenAPIServer) createProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.CreateProjectMachinePoolGrantRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
	poolID storage.ID,
) (openapi.CreateProjectMachinePoolGrantResponseObject, error) {
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	defaultCwd := ""
	if request.Body.DefaultCwd != nil {
		defaultCwd = *request.Body.DefaultCwd
	}
	defaultMachineEnvOverlay, err := rawJSONFromPointer(request.Body.DefaultMachineEnvOverlay)
	if err != nil {
		return nil, err
	}
	defaultMachineSecretEnvOverlay, err := rawJSONFromPointer(request.Body.DefaultMachineSecretEnvOverlay)
	if err != nil {
		return nil, err
	}
	defaultMachineProviderOptionsOverlay, err := rawJSONFromPointer(request.Body.DefaultMachineProviderOptionsOverlay)
	if err != nil {
		return nil, err
	}
	metadata := request.Body.Metadata
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	record, err := s.server.store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:                                org.ID,
			ProjectID:                            project.ID,
			MachinePoolID:                        poolID,
			Description:                          description,
			DefaultMachineCPU:                    intPtrFromInt32(request.Body.DefaultMachineCpu),
			DefaultMachineMemoryMB:               intPtrFromInt32(request.Body.DefaultMachineMemoryMb),
			DefaultMachineEnvOverlay:             defaultMachineEnvOverlay,
			DefaultMachineSecretEnvOverlay:       defaultMachineSecretEnvOverlay,
			DefaultMachineProviderOptionsOverlay: defaultMachineProviderOptionsOverlay,
			DefaultCwd:                           defaultCwd,
			MaxTotalMachines:                     intPtrFromInt32(request.Body.MaxTotalMachines),
			MaxTotalCPU:                          intPtrFromInt32(request.Body.MaxTotalCpu),
			MaxTotalMemoryMB:                     intPtrFromInt32(request.Body.MaxTotalMemoryMb),
			MinMachineCPU:                        intPtrFromInt32(request.Body.MinMachineCpu),
			MinMachineMemoryMB:                   intPtrFromInt32(request.Body.MinMachineMemoryMb),
			MaxMachineCPU:                        intPtrFromInt32(request.Body.MaxMachineCpu),
			MaxMachineMemoryMB:                   intPtrFromInt32(request.Body.MaxMachineMemoryMb),
			DeleteAfterIdleMinutes:               intPtrFromInt32(request.Body.DeleteAfterIdleMinutes),
			IdempotencyKey:                       idempotencyKey,
			Metadata:                             metadata,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectMachinePoolGrantResponse(record)
	if err != nil {
		return nil, err
	}
	if record.Created {
		return openapi.CreateProjectMachinePoolGrant201JSONResponse(response), nil
	}
	return openapi.CreateProjectMachinePoolGrant200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListProjectMachinePoolGrants(
	ctx context.Context,
	request openapi.ListProjectMachinePoolGrantsRequestObject,
) (openapi.ListProjectMachinePoolGrantsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listProjectMachinePoolGrants(ctx, request, scope.org, scope.project)
}

func (s strictOpenAPIServer) listProjectMachinePoolGrants(
	ctx context.Context,
	request openapi.ListProjectMachinePoolGrantsRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
) (openapi.ListProjectMachinePoolGrantsResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := org.ID.String() + "/" + project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_machine_pool_grants",
		Scope: scopeKey, IDKind: publicid.KindProjectMachinePoolGrant,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListProjectMachinePoolGrants(
		ctx,
		executionstore.ListProjectMachinePoolGrantsInput{OrgID: org.ID, ProjectID: project.ID, Limit: limit, List: list},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := make([]openapi.ProjectMachinePoolGrantListItem, 0, len(page.Grants))
	for _, record := range page.Grants {
		response, err := projectMachinePoolGrantResponse(record.Grant)
		if err != nil {
			return nil, err
		}
		pool, err := machinePoolSummaryResponse(record.MachinePool)
		if err != nil {
			return nil, err
		}
		out = append(out, openapi.ProjectMachinePoolGrantListItem{Grant: response, MachinePool: pool})
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "project_machine_pool_grants",
		scopeKey, publicid.KindProjectMachinePoolGrant, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectMachinePoolGrants200JSONResponse(
		openapi.ListProjectMachinePoolGrantsResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func machinePoolSummaryResponse(record executionstore.MachinePoolSummaryRecord) (openapi.MachinePoolSummary, error) {
	id, err := publicID(publicid.KindMachinePool, record.ID)
	if err != nil {
		return openapi.MachinePoolSummary{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.MachinePoolSummary{}, err
	}
	return openapi.MachinePoolSummary{
		Id:             id,
		OrgId:          orgID,
		Name:           record.Name,
		ManagementKind: openapi.ManagementKind(record.ManagementKind),
		Description:    record.Description,
		Provider:       record.Provider,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}, nil
}

func (s strictOpenAPIServer) GetProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.GetProjectMachinePoolGrantRequestObject,
) (openapi.GetProjectMachinePoolGrantResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getProjectMachinePoolGrant(ctx, request, scope.org, scope.project)
}

func (s strictOpenAPIServer) getProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.GetProjectMachinePoolGrantRequestObject,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
) (openapi.GetProjectMachinePoolGrantResponseObject, error) {
	poolGrantID, ok := parseOpenAPIPublicID(publicid.KindProjectMachinePoolGrant, request.PoolGrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().GetProjectMachinePoolGrant(ctx, org.ID, project.ID, poolGrantID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectMachinePoolGrantResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetProjectMachinePoolGrant200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.UpdateProjectMachinePoolGrantRequestObject,
) (openapi.UpdateProjectMachinePoolGrantResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !s.server.orgManageAllowed(ctx, scope.org.ID) {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	poolGrantID, ok := parseOpenAPIPublicID(publicid.KindProjectMachinePoolGrant, request.PoolGrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	defaultMachineEnvOverlay, err := rawJSONFromPointer(request.Body.DefaultMachineEnvOverlay)
	if err != nil {
		return nil, err
	}
	defaultMachineSecretEnvOverlay, err := rawJSONFromPointer(request.Body.DefaultMachineSecretEnvOverlay)
	if err != nil {
		return nil, err
	}
	defaultMachineProviderOptionsOverlay, err := rawJSONFromPointer(
		request.Body.DefaultMachineProviderOptionsOverlay,
	)
	if err != nil {
		return nil, err
	}
	metadata := request.Body.Metadata
	var envOverlayPatch, secretEnvOverlayPatch, providerOptionsOverlayPatch *json.RawMessage
	var metadataPatch *resourcemeta.Metadata
	if request.Body.DefaultMachineEnvOverlay != nil {
		envOverlayPatch = &defaultMachineEnvOverlay
	}
	if request.Body.DefaultMachineSecretEnvOverlay != nil {
		secretEnvOverlayPatch = &defaultMachineSecretEnvOverlay
	}
	if request.Body.DefaultMachineProviderOptionsOverlay != nil {
		providerOptionsOverlayPatch = &defaultMachineProviderOptionsOverlay
	}
	if request.Body.Metadata != nil {
		metadataPatch = &metadata
	}
	input := executionstore.UpdateProjectMachinePoolGrantInput{
		OrgID:                                scope.org.ID,
		ProjectID:                            scope.project.ID,
		ID:                                   poolGrantID,
		Description:                          request.Body.Description,
		DefaultMachineCPU:                    nullableIntPatchFromInt32(request.Body.DefaultMachineCpu),
		DefaultMachineMemoryMB:               nullableIntPatchFromInt32(request.Body.DefaultMachineMemoryMb),
		DefaultMachineEnvOverlay:             envOverlayPatch,
		DefaultMachineSecretEnvOverlay:       secretEnvOverlayPatch,
		DefaultMachineProviderOptionsOverlay: providerOptionsOverlayPatch,
		DefaultCwd:                           request.Body.DefaultCwd,
		MaxTotalMachines:                     nullableIntPatchFromInt32(request.Body.MaxTotalMachines),
		MaxTotalCPU:                          nullableIntPatchFromInt32(request.Body.MaxTotalCpu),
		MaxTotalMemoryMB:                     nullableIntPatchFromInt32(request.Body.MaxTotalMemoryMb),
		MinMachineCPU:                        nullableIntPatchFromInt32(request.Body.MinMachineCpu),
		MinMachineMemoryMB:                   nullableIntPatchFromInt32(request.Body.MinMachineMemoryMb),
		MaxMachineCPU:                        nullableIntPatchFromInt32(request.Body.MaxMachineCpu),
		MaxMachineMemoryMB:                   nullableIntPatchFromInt32(request.Body.MaxMachineMemoryMb),
		DeleteAfterIdleMinutes:               nullableIntPatchFromInt32(request.Body.DeleteAfterIdleMinutes),
		Metadata:                             metadataPatch,
	}
	record, err := s.server.store.Execution().UpdateProjectMachinePoolGrant(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectMachinePoolGrantResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateProjectMachinePoolGrant200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteProjectMachinePoolGrant(
	ctx context.Context,
	request openapi.DeleteProjectMachinePoolGrantRequestObject,
) (openapi.DeleteProjectMachinePoolGrantResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !s.server.orgManageAllowed(ctx, scope.org.ID) {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	poolGrantID, ok := parseOpenAPIPublicID(publicid.KindProjectMachinePoolGrant, request.PoolGrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return s.deleteProjectMachinePoolGrant(ctx, scope.org, scope.project, poolGrantID)
}

func (s strictOpenAPIServer) deleteProjectMachinePoolGrant(
	ctx context.Context,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
	poolGrantID storage.ID,
) (openapi.DeleteProjectMachinePoolGrantResponseObject, error) {
	result, err := s.server.store.Execution().DeleteProjectMachinePoolGrant(ctx, org.ID, project.ID, poolGrantID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, result.Machines)
	return openapi.DeleteProjectMachinePoolGrant204Response{}, nil
}

func projectMachinePoolGrantResponse(
	record executionstore.ProjectMachinePoolGrantRecord,
) (openapi.ProjectMachinePoolGrant, error) {
	id, err := publicID(publicid.KindProjectMachinePoolGrant, record.ID)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	poolID, err := publicID(publicid.KindMachinePool, record.MachinePoolID)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	metadata, err := resourcemeta.FromJSON(record.Metadata)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	var defaultMachineEnvOverlay map[string]*string
	if err := json.Unmarshal(record.DefaultMachineEnvOverlay, &defaultMachineEnvOverlay); err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	var defaultMachineSecretEnvOverlay map[string]*string
	if err := json.Unmarshal(record.DefaultMachineSecretEnvOverlay, &defaultMachineSecretEnvOverlay); err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	defaultMachineProviderOptionsOverlay, err := jsonMapOrFallback(
		record.DefaultMachineProviderOptionsOverlay,
		json.RawMessage(`{}`),
	)
	if err != nil {
		return openapi.ProjectMachinePoolGrant{}, err
	}
	return openapi.ProjectMachinePoolGrant{
		Id:                                   id,
		OrgId:                                orgID,
		ProjectId:                            projectID,
		MachinePoolId:                        poolID,
		Description:                          record.Description,
		DefaultMachineCpu:                    nullableInt32FromIntPtr(record.DefaultMachineCPU),
		DefaultMachineMemoryMb:               nullableInt32FromIntPtr(record.DefaultMachineMemoryMB),
		DefaultMachineEnvOverlay:             defaultMachineEnvOverlay,
		DefaultMachineSecretEnvOverlay:       defaultMachineSecretEnvOverlay,
		DefaultMachineProviderOptionsOverlay: defaultMachineProviderOptionsOverlay,
		DefaultCwd:                           record.DefaultCwd,
		MaxTotalMachines:                     nullableInt32FromIntPtr(record.MaxTotalMachines),
		MaxTotalCpu:                          nullableInt32FromIntPtr(record.MaxTotalCPU),
		MaxTotalMemoryMb:                     nullableInt32FromIntPtr(record.MaxTotalMemoryMB),
		MinMachineCpu:                        nullableInt32FromIntPtr(record.MinMachineCPU),
		MinMachineMemoryMb:                   nullableInt32FromIntPtr(record.MinMachineMemoryMB),
		MaxMachineCpu:                        nullableInt32FromIntPtr(record.MaxMachineCPU),
		MaxMachineMemoryMb:                   nullableInt32FromIntPtr(record.MaxMachineMemoryMB),
		DeleteAfterIdleMinutes:               nullableInt32FromIntPtr(record.DeleteAfterIdleMinutes),
		Metadata:                             metadata,
		CreatedAt:                            record.CreatedAt,
		UpdatedAt:                            record.UpdatedAt,
	}, nil
}
