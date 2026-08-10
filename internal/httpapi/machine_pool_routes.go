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

func (s *Server) machinePoolResponse(record executionstore.MachinePoolRecord) (openapi.MachinePool, error) {
	id, err := publicID(publicid.KindMachinePool, record.ID)
	if err != nil {
		return openapi.MachinePool{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.MachinePool{}, err
	}
	var defaultMachineEnv map[string]string
	if err := json.Unmarshal(record.DefaultMachineEnv, &defaultMachineEnv); err != nil {
		return openapi.MachinePool{}, err
	}
	var defaultMachineSecretEnv map[string]openapi.SecretID
	if err := json.Unmarshal(record.DefaultMachineSecretEnv, &defaultMachineSecretEnv); err != nil {
		return openapi.MachinePool{}, err
	}
	defaultMachineProviderOptions, err := jsonMapOrFallback(record.DefaultMachineProviderOptions, json.RawMessage(`{}`))
	if err != nil {
		return openapi.MachinePool{}, err
	}
	providerConfig, err := jsonMapOrFallback(record.ProviderConfig, json.RawMessage(`{}`))
	if err != nil {
		return openapi.MachinePool{}, err
	}
	metadata, err := jsonMapOrFallback(record.Metadata, json.RawMessage(`{}`))
	if err != nil {
		return openapi.MachinePool{}, err
	}
	response := openapi.MachinePool{
		Id:                            id,
		OrgId:                         orgID,
		Name:                          record.Name,
		ManagementKind:                openapi.ManagementKind(record.ManagementKind),
		Description:                   record.Description,
		Provider:                      record.Provider,
		DefaultMachineCpu:             nullableInt32FromIntPtr(record.DefaultMachineCPU),
		DefaultMachineMemoryMb:        nullableInt32FromIntPtr(record.DefaultMachineMemoryMB),
		DefaultMachineEnv:             defaultMachineEnv,
		DefaultMachineSecretEnv:       defaultMachineSecretEnv,
		DefaultMachineProviderOptions: defaultMachineProviderOptions,
		DefaultCwd:                    record.DefaultCwd,
		ProviderConfig:                providerConfig,
		RuntimeProtectionEnabled:      record.RuntimeProtectionEnabled,
		MaxTotalMachines:              record.MaxTotalMachines,
		MaxTotalCpu:                   nullableInt32FromIntPtr(record.MaxTotalCPU),
		MaxTotalMemoryMb:              nullableInt32FromIntPtr(record.MaxTotalMemoryMB),
		MaxMachineCpu:                 nullableInt32FromIntPtr(record.MaxMachineCPU),
		MaxMachineMemoryMb:            nullableInt32FromIntPtr(record.MaxMachineMemoryMB),
		Metadata:                      metadata,
		CreatedAt:                     record.CreatedAt,
		UpdatedAt:                     record.UpdatedAt,
	}
	if record.ProviderAuthSecretID != storage.NilID {
		secretID, err := publicID(publicid.KindSecret, record.ProviderAuthSecretID)
		if err != nil {
			return openapi.MachinePool{}, err
		}
		response.ProviderAuthSecretId = &secretID
	}
	return response, nil
}

func (s strictOpenAPIServer) ListMachinePools(
	ctx context.Context,
	request openapi.ListMachinePoolsRequestObject,
) (openapi.ListMachinePoolsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listMachinePools(ctx, request, org)
}

func (s strictOpenAPIServer) listMachinePools(
	ctx context.Context,
	request openapi.ListMachinePoolsRequestObject,
	org identitystore.OrgRecord,
) (openapi.ListMachinePoolsResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "machine_pools",
		Scope: org.ID.String(), IDKind: publicid.KindMachinePool,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListMachinePools(ctx, executionstore.ListMachinePoolsInput{
		OrgID: org.ID,
		Limit: limit,
		List:  list,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	out := make([]openapi.MachinePool, 0, len(page.Pools))
	for _, record := range page.Pools {
		response, err := s.server.machinePoolResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "machine_pools",
		org.ID.String(), publicid.KindMachinePool, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListMachinePools200JSONResponse(
		openapi.ListMachinePoolsResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) CreateMachinePool(
	ctx context.Context,
	request openapi.CreateMachinePoolRequestObject,
) (openapi.CreateMachinePoolResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createMachinePool(ctx, request, org)
}

func (s strictOpenAPIServer) createMachinePool(
	ctx context.Context,
	request openapi.CreateMachinePoolRequestObject,
	org identitystore.OrgRecord,
) (openapi.CreateMachinePoolResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	defaultCwd := ""
	if request.Body.DefaultCwd != nil {
		defaultCwd = *request.Body.DefaultCwd
	}
	defaultMachineEnv, err := rawJSONFromPointer(request.Body.DefaultMachineEnv)
	if err != nil {
		return nil, err
	}
	defaultMachineSecretEnv, err := rawJSONFromPointer(request.Body.DefaultMachineSecretEnv)
	if err != nil {
		return nil, err
	}
	defaultMachineProviderOptions, err := rawJSONFromPointer(&request.Body.DefaultMachineProviderOptions)
	if err != nil {
		return nil, err
	}
	providerConfig, err := rawJSONFromPointer(request.Body.ProviderConfig)
	if err != nil {
		return nil, err
	}
	metadata, err := rawJSONFromPointer(request.Body.Metadata)
	if err != nil {
		return nil, err
	}
	providerAuthSecretID, ok := parseOpenAPIPublicID(publicid.KindSecret, request.Body.ProviderAuthSecretId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid provider_auth_secret_id")
	}
	runtimeProtectionEnabled := request.Body.RuntimeProtectionEnabled != nil &&
		*request.Body.RuntimeProtectionEnabled
	record, err := s.server.store.Execution().CreateMachinePool(ctx, executionstore.CreateMachinePoolInput{
		OrgID:                         org.ID,
		Name:                          request.Body.Name,
		Description:                   description,
		Provider:                      request.Body.Provider,
		DefaultMachineCPU:             intPtrFromInt32(request.Body.DefaultMachineCpu),
		DefaultMachineMemoryMB:        intPtrFromInt32(request.Body.DefaultMachineMemoryMb),
		DefaultMachineEnv:             defaultMachineEnv,
		DefaultMachineSecretEnv:       defaultMachineSecretEnv,
		DefaultMachineProviderOptions: defaultMachineProviderOptions,
		DefaultCwd:                    defaultCwd,
		ProviderConfig:                providerConfig,
		ProviderAuthSecretID:          providerAuthSecretID,
		RuntimeProtectionEnabled:      runtimeProtectionEnabled,
		MaxTotalMachines:              request.Body.MaxTotalMachines,
		MaxTotalCPU:                   intPtrFromInt32(request.Body.MaxTotalCpu),
		MaxTotalMemoryMB:              intPtrFromInt32(request.Body.MaxTotalMemoryMb),
		MaxMachineCPU:                 intPtrFromInt32(request.Body.MaxMachineCpu),
		MaxMachineMemoryMB:            intPtrFromInt32(request.Body.MaxMachineMemoryMb),
		Metadata:                      metadata,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := s.server.machinePoolResponse(record)
	if err != nil {
		return nil, err
	}
	if record.Created {
		return openapi.CreateMachinePool201JSONResponse(response), nil
	}
	return openapi.CreateMachinePool200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetMachinePool(
	ctx context.Context,
	request openapi.GetMachinePoolRequestObject,
) (openapi.GetMachinePoolResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getMachinePool(ctx, request, org)
}

func (s strictOpenAPIServer) getMachinePool(
	ctx context.Context,
	request openapi.GetMachinePoolRequestObject,
	org identitystore.OrgRecord,
) (openapi.GetMachinePoolResponseObject, error) {
	poolID, ok := parseOpenAPIPublicID(publicid.KindMachinePool, request.PoolID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().GetMachinePool(ctx, org.ID, poolID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := s.server.machinePoolResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetMachinePool200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateMachinePool(
	ctx context.Context,
	request openapi.UpdateMachinePoolRequestObject,
) (openapi.UpdateMachinePoolResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.updateMachinePool(ctx, request, org)
}

func (s strictOpenAPIServer) updateMachinePool(
	ctx context.Context,
	request openapi.UpdateMachinePoolRequestObject,
	org identitystore.OrgRecord,
) (openapi.UpdateMachinePoolResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	poolID, ok := parseOpenAPIPublicID(publicid.KindMachinePool, request.PoolID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	defaultMachineEnv, err := rawJSONFromPointer(request.Body.DefaultMachineEnv)
	if err != nil {
		return nil, err
	}
	defaultMachineSecretEnv, err := rawJSONFromPointer(request.Body.DefaultMachineSecretEnv)
	if err != nil {
		return nil, err
	}
	defaultMachineProviderOptions, err := rawJSONFromPointer(request.Body.DefaultMachineProviderOptions)
	if err != nil {
		return nil, err
	}
	providerConfig, err := rawJSONFromPointer(request.Body.ProviderConfig)
	if err != nil {
		return nil, err
	}
	metadata, err := rawJSONFromPointer(request.Body.Metadata)
	if err != nil {
		return nil, err
	}
	var providerAuthSecretID *storage.ID
	if request.Body.ProviderAuthSecretId != nil {
		parsed, ok := parseOpenAPIPublicID(publicid.KindSecret, *request.Body.ProviderAuthSecretId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid provider_auth_secret_id")
		}
		providerAuthSecretID = &parsed
	}
	record, err := s.server.store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID:                         org.ID,
		ID:                            poolID,
		Name:                          request.Body.Name,
		Description:                   request.Body.Description,
		DefaultMachineCPU:             nullableIntPatchFromInt32(request.Body.DefaultMachineCpu),
		DefaultMachineMemoryMB:        nullableIntPatchFromInt32(request.Body.DefaultMachineMemoryMb),
		DefaultMachineEnv:             defaultMachineEnv,
		DefaultMachineSecretEnv:       defaultMachineSecretEnv,
		DefaultMachineProviderOptions: defaultMachineProviderOptions,
		DefaultCwd:                    request.Body.DefaultCwd,
		ProviderConfig:                providerConfig,
		ProviderAuthSecretID:          providerAuthSecretID,
		RuntimeProtectionEnabled:      request.Body.RuntimeProtectionEnabled,
		MaxTotalMachines:              request.Body.MaxTotalMachines,
		MaxTotalCPU:                   nullableIntPatchFromInt32(request.Body.MaxTotalCpu),
		MaxTotalMemoryMB:              nullableIntPatchFromInt32(request.Body.MaxTotalMemoryMb),
		MaxMachineCPU:                 nullableIntPatchFromInt32(request.Body.MaxMachineCpu),
		MaxMachineMemoryMB:            nullableIntPatchFromInt32(request.Body.MaxMachineMemoryMb),
		Metadata:                      metadata,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := s.server.machinePoolResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateMachinePool200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteMachinePool(
	ctx context.Context,
	request openapi.DeleteMachinePoolRequestObject,
) (openapi.DeleteMachinePoolResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.deleteMachinePool(ctx, request, org)
}

func (s strictOpenAPIServer) deleteMachinePool(
	ctx context.Context,
	request openapi.DeleteMachinePoolRequestObject,
	org identitystore.OrgRecord,
) (openapi.DeleteMachinePoolResponseObject, error) {
	poolID, ok := parseOpenAPIPublicID(publicid.KindMachinePool, request.PoolID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	machines, err := s.server.store.Execution().DeleteMachinePool(ctx, org.ID, poolID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, machines)
	return openapi.DeleteMachinePool204Response{}, nil
}
