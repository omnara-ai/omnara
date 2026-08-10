package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) CreateMachine(
	ctx context.Context,
	request openapi.CreateMachineRequestObject,
) (openapi.CreateMachineResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createMachine(ctx, request, org)
}

func (s strictOpenAPIServer) createMachine(
	ctx context.Context,
	request openapi.CreateMachineRequestObject,
	org identitystore.OrgRecord,
) (openapi.CreateMachineResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	metadata, err := rawJSONFromPointer(request.Body.Metadata)
	if err != nil {
		return nil, err
	}
	env, err := rawJSONFromPointer(request.Body.Env)
	if err != nil {
		return nil, err
	}
	secretEnv, err := rawJSONFromPointer(request.Body.SecretEnv)
	if err != nil {
		return nil, err
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	record, err := s.server.store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          org.ID,
		DisplayName:    request.Body.DisplayName,
		Description:    description,
		Cwd:            stringValue(request.Body.Cwd),
		Env:            env,
		SecretEnv:      secretEnv,
		IdempotencyKey: idempotencyKey,
		Metadata:       metadata,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := machineResponse(record)
	if err != nil {
		return nil, err
	}
	if record.Created {
		return openapi.CreateMachine201JSONResponse(response), nil
	}
	return openapi.CreateMachine200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateMachine(
	ctx context.Context,
	request openapi.UpdateMachineRequestObject,
) (openapi.UpdateMachineResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	env, err := rawJSONFromPointer(request.Body.Env)
	if err != nil {
		return nil, err
	}
	secretEnv, err := rawJSONFromPointer(request.Body.SecretEnv)
	if err != nil {
		return nil, err
	}
	var envUpdate, secretEnvUpdate *json.RawMessage
	if request.Body.Env != nil {
		envUpdate = &env
	}
	if request.Body.SecretEnv != nil {
		secretEnvUpdate = &secretEnv
	}
	record, err := s.server.store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
		OrgID:     machine.OrgID,
		MachineID: machine.ID,
		Cwd:       request.Body.Cwd,
		Env:       envUpdate,
		SecretEnv: secretEnvUpdate,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := machineResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateMachine200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListVisibleMachines(
	ctx context.Context,
	request openapi.ListVisibleMachinesRequestObject,
) (openapi.ListVisibleMachinesResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listVisibleMachines(ctx, request, org)
}

func (s strictOpenAPIServer) listVisibleMachines(
	ctx context.Context,
	request openapi.ListVisibleMachinesRequestObject,
	org identitystore.OrgRecord,
) (openapi.ListVisibleMachinesResponseObject, error) {
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
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "machines",
		Scope: org.ID.String(), IDKind: publicid.KindMachine,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{
			OrgID: org.ID, Principal: principal, Filters: filters, Limit: limit, List: list,
		},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	out := make([]openapi.VisibleMachine, 0, len(page.Machines))
	for _, record := range page.Machines {
		response, err := visibleMachineResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "machines", org.ID.String(), publicid.KindMachine, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListVisibleMachines200JSONResponse(
		openapi.ListVisibleMachinesResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) GetMachine(
	ctx context.Context,
	request openapi.GetMachineRequestObject,
) (openapi.GetMachineResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.getMachine(ctx, request, machine)
}

func (s strictOpenAPIServer) getMachine(
	ctx context.Context,
	request openapi.GetMachineRequestObject,
	machine executionstore.MachineRecord,
) (openapi.GetMachineResponseObject, error) {
	if machine.DeletedAt != nil || machine.LifecycleState != executionstore.MachineLifecycleStateActive {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := machineResponse(machine)
	if err != nil {
		return nil, err
	}
	return openapi.GetMachine200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteMachine(
	ctx context.Context,
	request openapi.DeleteMachineRequestObject,
) (openapi.DeleteMachineResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.deleteMachine(ctx, request, machine)
}

func (s strictOpenAPIServer) deleteMachine(
	ctx context.Context,
	request openapi.DeleteMachineRequestObject,
	machine executionstore.MachineRecord,
) (openapi.DeleteMachineResponseObject, error) {
	if machine.SourceKind != executionstore.MachineSourceKindBYO {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.store.Execution().DeleteMachine(ctx, executionstore.DeleteMachineInput{
		OrgID:     machine.OrgID,
		MachineID: machine.ID,
	}); err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.DeleteMachine204Response{}, nil
}

func machineResponse(record executionstore.MachineRecord) (openapi.Machine, error) {
	id, err := publicID(publicid.KindMachine, record.ID)
	if err != nil {
		return openapi.Machine{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.Machine{}, err
	}
	machinePoolID, err := idOrNil(publicid.KindMachinePool, record.MachinePoolID)
	if err != nil {
		return openapi.Machine{}, err
	}
	metadata, err := jsonMapOrFallback(record.Metadata, json.RawMessage(`{}`))
	if err != nil {
		return openapi.Machine{}, err
	}
	var env map[string]string
	if err := json.Unmarshal(record.Env, &env); err != nil {
		return openapi.Machine{}, err
	}
	var secretEnv map[string]string
	if err := json.Unmarshal(record.SecretEnv, &secretEnv); err != nil {
		return openapi.Machine{}, err
	}
	return openapi.Machine{
		Id:                     id,
		OrgId:                  orgID,
		SourceKind:             openapi.MachineSourceKind(record.SourceKind),
		MachinePoolId:          nullableFromPtr(machinePoolID),
		DisplayName:            record.DisplayName,
		Description:            record.Description,
		Provider:               record.Provider,
		LifecycleState:         openapi.MachineLifecycleState(record.LifecycleState),
		ConnectionState:        openapi.MachineConnectionState(record.ConnectionState),
		ConnectionStateReason:  ptrFromNonEmpty(record.ConnectionStateReason),
		LastObservedAt:         nullableFromPtr(record.LastObservedAt),
		Cwd:                    record.Cwd,
		Env:                    env,
		SecretEnv:              secretEnv,
		LifecycleReasonCode:    record.LifecycleReasonCode,
		LifecycleReasonMessage: record.LifecycleReasonMessage,
		NextReconcileAfter:     nullableFromPtr(record.NextReconcileAfter),
		ProvisionAttempts:      record.ProvisionAttempts,
		DeleteAttempts:         record.DeleteAttempts,
		Metadata:               metadata,
		DeletedAt:              nullableFromPtr(record.DeletedAt),
		CreatedAt:              record.CreatedAt,
		UpdatedAt:              record.UpdatedAt,
	}, nil
}

func visibleMachineResponse(record executionstore.VisibleMachineRecord) (openapi.VisibleMachine, error) {
	response, err := machineSummaryResponse(record.Machine)
	if err != nil {
		return openapi.VisibleMachine{}, err
	}
	sources, err := machineAccessSourceResponses(record.Sources)
	if err != nil {
		return openapi.VisibleMachine{}, err
	}
	return openapi.VisibleMachine{
		Id:              response.Id,
		OrgId:           response.OrgId,
		SourceKind:      response.SourceKind,
		DisplayName:     response.DisplayName,
		Description:     response.Description,
		Provider:        response.Provider,
		LifecycleState:  response.LifecycleState,
		ConnectionState: response.ConnectionState,
		LastObservedAt:  response.LastObservedAt,
		DeletedAt:       response.DeletedAt,
		CreatedAt:       response.CreatedAt,
		UpdatedAt:       response.UpdatedAt,
		Access:          openapi.MachineAccess{CanManage: record.CanManage, Sources: sources},
	}, nil
}

func projectVisibleMachineResponse(
	projectID storage.ID,
	record executionstore.ProjectVisibleMachineRecord,
) (openapi.VisibleMachine, error) {
	summary, err := machineSummaryResponse(record.Machine)
	if err != nil {
		return openapi.VisibleMachine{}, err
	}
	source := executionstore.MachineAccessSourceRecord{
		Kind:            executionstore.MachineAccessSourceKindProjectMachineGrant,
		ProjectID:       projectID,
		GrantID:         record.GrantID,
		GrantSourceKind: record.GrantSourceKind,
	}
	sources, err := machineAccessSourceResponses([]executionstore.MachineAccessSourceRecord{source})
	if err != nil {
		return openapi.VisibleMachine{}, err
	}
	return openapi.VisibleMachine{
		Id:              summary.Id,
		OrgId:           summary.OrgId,
		SourceKind:      summary.SourceKind,
		DisplayName:     summary.DisplayName,
		Description:     summary.Description,
		Provider:        summary.Provider,
		LifecycleState:  summary.LifecycleState,
		ConnectionState: summary.ConnectionState,
		LastObservedAt:  summary.LastObservedAt,
		DeletedAt:       summary.DeletedAt,
		CreatedAt:       summary.CreatedAt,
		UpdatedAt:       summary.UpdatedAt,
		Access:          openapi.MachineAccess{CanManage: record.CanManage, Sources: sources},
	}, nil
}

func machineSummaryResponse(record executionstore.MachineSummaryRecord) (openapi.MachineSummary, error) {
	id, err := publicID(publicid.KindMachine, record.ID)
	if err != nil {
		return openapi.MachineSummary{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.MachineSummary{}, err
	}
	return openapi.MachineSummary{
		Id:              id,
		OrgId:           orgID,
		SourceKind:      openapi.MachineSourceKind(record.SourceKind),
		DisplayName:     record.DisplayName,
		Description:     record.Description,
		Provider:        record.Provider,
		LifecycleState:  openapi.MachineLifecycleState(record.LifecycleState),
		ConnectionState: openapi.MachineConnectionState(record.ConnectionState),
		LastObservedAt:  nullableFromPtr(record.LastObservedAt),
		DeletedAt:       nullableFromPtr(record.DeletedAt),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}, nil
}

func machineAccessSourceResponses(
	records []executionstore.MachineAccessSourceRecord,
) ([]openapi.MachineAccessSource, error) {
	out := make([]openapi.MachineAccessSource, 0, len(records))
	for _, record := range records {
		response := openapi.MachineAccessSource{Kind: openapi.MachineAccessSourceKind(record.Kind)}
		if record.ProjectID != storage.NilID {
			projectID, err := publicID(publicid.KindProject, record.ProjectID)
			if err != nil {
				return nil, err
			}
			response.ProjectId = &projectID
		}
		if record.GrantID != storage.NilID {
			grantID, err := publicID(publicid.KindProjectMachineGrant, record.GrantID)
			if err != nil {
				return nil, err
			}
			response.GrantId = &grantID
		}
		if record.GrantSourceKind != "" {
			grantSourceKind := openapi.ProjectMachineGrantSourceKind(record.GrantSourceKind)
			response.GrantSourceKind = &grantSourceKind
		}
		out = append(out, response)
	}
	return out, nil
}

func machineDaemonTokenResponse(record executionstore.MachineDaemonTokenRecord) (openapi.MachineDaemonToken, error) {
	id, err := publicID(publicid.KindMachineDaemonToken, record.ID)
	if err != nil {
		return openapi.MachineDaemonToken{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.MachineDaemonToken{}, err
	}
	machineID, err := publicID(publicid.KindMachine, record.MachineID)
	if err != nil {
		return openapi.MachineDaemonToken{}, err
	}
	metadata, err := jsonMapOrFallback(record.Metadata, json.RawMessage(`{}`))
	if err != nil {
		return openapi.MachineDaemonToken{}, err
	}
	return openapi.MachineDaemonToken{
		Id:           id,
		OrgId:        orgID,
		MachineId:    machineID,
		Name:         record.Name,
		Metadata:     metadata,
		CreatedAt:    record.CreatedAt,
		LastUsedAt:   nullableFromPtr(record.LastUsedAt),
		RevokedAt:    nullableFromPtr(record.RevokedAt),
		RevokeReason: record.RevokeReason,
	}, nil
}

func newDaemonToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return executionstore.MachineDaemonTokenPlaintextPrefix + hex.EncodeToString(b[:]), nil
}

func (s strictOpenAPIServer) ListBYOMachineDaemonTokens(
	ctx context.Context,
	request openapi.ListBYOMachineDaemonTokensRequestObject,
) (openapi.ListBYOMachineDaemonTokensResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listBYOMachineDaemonTokens(ctx, request, machine)
}

func (s strictOpenAPIServer) listBYOMachineDaemonTokens(
	ctx context.Context,
	request openapi.ListBYOMachineDaemonTokensRequestObject,
	machine executionstore.MachineRecord,
) (openapi.ListBYOMachineDaemonTokensResponseObject, error) {
	if machine.SourceKind != executionstore.MachineSourceKindBYO {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindMachineDaemonToken,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListBYOMachineDaemonTokens(ctx, executionstore.ListBYOMachineDaemonTokensInput{
		OrgID:     machine.OrgID,
		MachineID: machine.ID,
		Limit:     limit,
		After:     after,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	out := make([]openapi.MachineDaemonToken, 0, len(page.Tokens))
	var last executionstore.MachineDaemonTokenRecord
	for _, record := range page.Tokens {
		response, err := machineDaemonTokenResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
		last = record
	}
	nextCursor, err := encodeNextCursor(page.HasMore, last.CreatedAt, publicid.KindMachineDaemonToken, last.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListBYOMachineDaemonTokens200JSONResponse(
		openapi.ListMachineDaemonTokensResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) CreateBYOMachineDaemonToken(
	ctx context.Context,
	request openapi.CreateBYOMachineDaemonTokenRequestObject,
) (openapi.CreateBYOMachineDaemonTokenResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createBYOMachineDaemonToken(ctx, request, machine)
}

func (s strictOpenAPIServer) createBYOMachineDaemonToken(
	ctx context.Context,
	request openapi.CreateBYOMachineDaemonTokenRequestObject,
	machine executionstore.MachineRecord,
) (openapi.CreateBYOMachineDaemonTokenResponseObject, error) {
	if machine.SourceKind != executionstore.MachineSourceKindBYO {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	body := openapi.CreateBYOMachineDaemonTokenJSONRequestBody{}
	if request.Body != nil {
		body = *request.Body
	}
	name := "daemon"
	if body.Name != nil && *body.Name != "" {
		name = *body.Name
	}
	metadata, err := rawJSONFromPointer(body.Metadata)
	if err != nil {
		return nil, err
	}
	token, err := newDaemonToken()
	if err != nil {
		return nil, err
	}
	record, err := s.server.store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     machine.OrgID,
			MachineID: machine.ID,
			Name:      name,
			Token:     token,
			Metadata:  metadata,
		},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	tokenRecord, err := machineDaemonTokenResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateBYOMachineDaemonToken201JSONResponse(
		openapi.CreateMachineDaemonTokenResponse{
			Token:       token,
			TokenRecord: tokenRecord,
		},
	), nil
}

func (s strictOpenAPIServer) RevokeMachineDaemonToken(
	ctx context.Context,
	request openapi.RevokeMachineDaemonTokenRequestObject,
) (openapi.RevokeMachineDaemonTokenResponseObject, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.revokeMachineDaemonToken(ctx, request, machine)
}

func (s strictOpenAPIServer) revokeMachineDaemonToken(
	ctx context.Context,
	request openapi.RevokeMachineDaemonTokenRequestObject,
	machine executionstore.MachineRecord,
) (openapi.RevokeMachineDaemonTokenResponseObject, error) {
	if machine.SourceKind != executionstore.MachineSourceKindBYO {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	tokenID, ok := parseOpenAPIPublicID(publicid.KindMachineDaemonToken, request.TokenID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		machine.OrgID,
		machine.ID,
		tokenID,
		"revoked",
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := machineDaemonTokenResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.RevokeMachineDaemonToken200JSONResponse(response), nil
}
