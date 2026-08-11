package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) BootstrapDaemon(
	ctx context.Context,
	_ openapi.BootstrapDaemonRequestObject,
) (openapi.BootstrapDaemonResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	bootstrap, err := s.server.store.Execution().BootstrapMachineDaemon(
		ctx,
		executionstore.MachineDaemonBootstrapInput{
			OrgID:         scope.OrgID,
			MachineID:     scope.MachineID,
			DaemonTokenID: scope.DaemonTokenID,
		},
	)
	if err != nil {
		responseErr := apierror.OrgScoped(err)
		if responseErr.Status == http.StatusBadRequest {
			responseErr = apierror.FromCode(
				openapi.ErrorCodeServiceUnavailable,
				"daemon bootstrap unavailable",
			)
		}
		return nil, responseErr
	}
	logent.MachineBootstrap(ctx, bootstrap)
	response, err := daemonBootstrapResponse(bootstrap)
	if err != nil {
		return nil, err
	}
	return openapi.BootstrapDaemon200JSONResponse(response), nil
}

func (s strictOpenAPIServer) RecordMachineFailure(
	ctx context.Context,
	request openapi.RecordMachineFailureRequestObject,
) (openapi.RecordMachineFailureResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	var outputTail []byte
	if request.Body != nil {
		outputTail = []byte(*request.Body)
	}
	captureFailed := request.Params.CaptureStatus != nil && *request.Params.CaptureStatus != 0
	outputTruncated := captureFailed ||
		len(outputTail) > executionstore.MaxMachineFailureReportOutputBytes
	if len(outputTail) > executionstore.MaxMachineFailureReportOutputBytes {
		outputTail = outputTail[len(outputTail)-executionstore.MaxMachineFailureReportOutputBytes:]
	}
	report := executionstore.MachineFailureReportInput{
		OrgID:           scope.OrgID,
		MachineID:       scope.MachineID,
		DaemonTokenID:   scope.DaemonTokenID,
		Stage:           string(request.Params.Stage),
		ExitStatus:      request.Params.ExitStatus,
		OutputTail:      outputTail,
		OutputTruncated: outputTruncated,
		DaemonVersion:   stringFromPtr(request.Params.DaemonVersion),
		TargetVersion:   stringFromPtr(request.Params.TargetVersion),
	}
	err := s.server.store.Execution().RecordMachineFailureReport(ctx, report)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	logent.MachineFailureReport(ctx, report)
	return openapi.RecordMachineFailure204Response{}, nil
}

func daemonBootstrapResponse(bootstrap executionstore.MachineBootstrapRecord) (openapi.BootstrapDaemonResponse, error) {
	installationID, err := publicID(publicid.KindInstallation, bootstrap.InstallationID)
	if err != nil {
		return openapi.BootstrapDaemonResponse{}, err
	}
	machineID, err := publicID(publicid.KindMachine, bootstrap.MachineID)
	if err != nil {
		return openapi.BootstrapDaemonResponse{}, err
	}
	response := openapi.BootstrapDaemonResponse{
		InstallationId: installationID,
		MachineId:      machineID,
	}
	return response, nil
}

func machineDaemonScopeFromContext(ctx context.Context) (machineDaemonScope, *apierror.ResponseError) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeMachineDaemon || principal.OrgID == storage.NilID ||
		principal.ID == storage.NilID || principal.MachineDaemonTokenID == storage.NilID ||
		principal.ProjectID != storage.NilID {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return machineDaemonScope{}, &err
	}
	return machineDaemonScope{
		OrgID:         principal.OrgID,
		MachineID:     principal.ID,
		DaemonTokenID: principal.MachineDaemonTokenID,
	}, nil
}

func (s strictOpenAPIServer) RegisterMachineDaemonRuntime(
	ctx context.Context,
	request openapi.RegisterMachineDaemonRuntimeRequestObject,
) (openapi.RegisterMachineDaemonRuntimeResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	return s.registerMachineDaemonRuntime(ctx, request, scope)
}

func (s strictOpenAPIServer) registerMachineDaemonRuntime(
	ctx context.Context,
	request openapi.RegisterMachineDaemonRuntimeRequestObject,
	scope machineDaemonScope,
) (openapi.RegisterMachineDaemonRuntimeResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	body := request.Body
	if body.DaemonInstanceId == uuid.Nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "daemon_instance_id is required")
	}
	if err := daemonversion.Validate(body.DaemonVersion); err != nil {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"invalid daemon_version: "+err.Error(),
		)
	}
	claims := make([]executionstore.ProcessReconciliationClaim, 0, len(body.Processes))
	seenProcesses := make(map[storage.ID]struct{}, len(body.Processes))
	for _, claim := range body.Processes {
		id, ok := parseOpenAPIPublicID(publicid.KindProcess, claim.ProcessId)
		if !ok {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid process claim process_id",
			)
		}
		if _, duplicate := seenProcesses[id]; duplicate {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"duplicate process claim process_id",
			)
		}
		seenProcesses[id] = struct{}{}
		if claim.SupervisorInstanceId == "" || !claim.Phase.Valid() {
			return nil, apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"invalid process claim supervisor instance ID or phase",
			)
		}
		actions := make(
			[]executionstore.ProcessActionReconciliationClaim,
			0,
			len(claim.Actions),
		)
		seenActions := make(map[storage.ID]struct{}, len(claim.Actions))
		seenSeq := make(map[int64]struct{}, len(claim.Actions))
		for _, action := range claim.Actions {
			actionID, ok := parseOpenAPIPublicID(
				publicid.KindProcessAction,
				action.ProcessActionId,
			)
			if !ok || action.Seq <= 0 ||
				!action.ActionKind.Valid() ||
				!action.Position.Valid() {
				return nil, apierror.FromCode(
					openapi.ErrorCodeInvalidRequest,
					"invalid process action claim",
				)
			}
			if _, duplicate := seenActions[actionID]; duplicate {
				return nil, apierror.FromCode(
					openapi.ErrorCodeInvalidRequest,
					"duplicate process action claim process_action_id",
				)
			}
			if _, duplicate := seenSeq[action.Seq]; duplicate {
				return nil, apierror.FromCode(
					openapi.ErrorCodeInvalidRequest,
					"duplicate process action claim sequence",
				)
			}
			seenActions[actionID] = struct{}{}
			seenSeq[action.Seq] = struct{}{}
			actions = append(actions, executionstore.ProcessActionReconciliationClaim{
				ProcessActionID: actionID,
				Seq:             action.Seq,
				ActionKind: executionstore.ProcessActionKind(
					action.ActionKind,
				),
				Position: daemonprotocol.ActionPosition(action.Position),
			})
		}
		claims = append(claims, executionstore.ProcessReconciliationClaim{
			ProcessID:             id,
			SupervisorInstanceID:  claim.SupervisorInstanceId,
			Phase:                 daemonprotocol.ProcessPhase(claim.Phase),
			SupervisorLive:        claim.SupervisorLive,
			ExecutionCommitted:    claim.ExecutionCommitted,
			ActionAdmissionClosed: claim.ActionAdmissionClosed,
			ResolvedActionSeq:     claim.ResolvedActionSeq,
			Actions:               actions,
		})
	}
	capacity, err := rawJSONFromPointer(body.Capacity)
	if err != nil {
		return nil, err
	}
	metadata, err := rawJSONFromPointer(body.Metadata)
	if err != nil {
		return nil, err
	}
	observedPlatform, err := rawJSONFromPointer(body.ObservedPlatform)
	if err != nil {
		return nil, err
	}
	record, err := s.server.store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            scope.OrgID,
			MachineID:        scope.MachineID,
			DaemonTokenID:    scope.DaemonTokenID,
			DaemonInstanceID: body.DaemonInstanceId,
			DaemonVersion:    body.DaemonVersion,
			Capacity:         capacity,
			Metadata:         metadata,
			ObservedPlatform: observedPlatform,
			ProcessClaims:    claims,
			LeaseTimeout:     s.server.daemonRuntimeLeaseDuration,
		},
	)
	if err != nil {
		responseErr := apierror.OrgScoped(err)
		if responseErr.Status == http.StatusBadRequest || responseErr.Status == http.StatusNotFound {
			responseErr = apierror.FromCode(
				openapi.ErrorCodeServiceUnavailable,
				"daemon registration unavailable",
			)
		}
		return nil, responseErr
	}
	logent.DaemonRuntimeRegistration(ctx, record)
	response, err := daemonRuntimeResponse(record.Runtime)
	if err != nil {
		return nil, err
	}
	reconciliation, err := daemonRuntimeReconciliationResponse(record.Reconciliation)
	if err != nil {
		return nil, err
	}
	return openapi.RegisterMachineDaemonRuntime201JSONResponse{Runtime: response, Reconciliation: reconciliation}, nil
}

func (s strictOpenAPIServer) EndMachineDaemonRuntime(
	ctx context.Context,
	request openapi.EndMachineDaemonRuntimeRequestObject,
) (openapi.EndMachineDaemonRuntimeResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	return s.endMachineDaemonRuntime(ctx, request, scope)
}

func (s strictOpenAPIServer) endMachineDaemonRuntime(
	ctx context.Context,
	request openapi.EndMachineDaemonRuntimeRequestObject,
	scope machineDaemonScope,
) (openapi.EndMachineDaemonRuntimeResponseObject, error) {
	runtimeID, ok := parseOpenAPIPublicID(publicid.KindDaemonRuntime, request.RuntimeID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().EndDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           scope.OrgID,
			MachineID:       scope.MachineID,
			DaemonRuntimeID: runtimeID,
			DaemonTokenID:   scope.DaemonTokenID,
		},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	logent.DaemonRuntime(ctx, record)
	response, err := daemonRuntimeResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.EndMachineDaemonRuntime200JSONResponse(response), nil
}

func (s strictOpenAPIServer) SleepMachineDaemonRuntime(
	ctx context.Context,
	request openapi.SleepMachineDaemonRuntimeRequestObject,
) (openapi.SleepMachineDaemonRuntimeResponseObject, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	runtimeID, ok := parseOpenAPIPublicID(publicid.KindDaemonRuntime, request.RuntimeID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().SleepDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           scope.OrgID,
			MachineID:       scope.MachineID,
			DaemonRuntimeID: runtimeID,
			DaemonTokenID:   scope.DaemonTokenID,
		},
	)
	if errors.Is(err, storeerr.ErrMachineSleepPendingWork) {
		return openapi.SleepMachineDaemonRuntime409JSONResponse{
			Code:  openapi.ErrorCodePendingWork,
			Error: err.Error(),
		}, nil
	}
	if errors.Is(err, storeerr.ErrMachineNotWakeCapable) {
		return openapi.SleepMachineDaemonRuntime409JSONResponse{
			Code:  openapi.ErrorCodeNotWakeCapable,
			Error: err.Error(),
		}, nil
	}
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	logent.DaemonRuntime(ctx, record)
	response, err := daemonRuntimeResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.SleepMachineDaemonRuntime200JSONResponse(response), nil
}

func daemonRuntimeResponse(record executionstore.DaemonRuntimeRecord) (openapi.DaemonRuntime, error) {
	id, err := publicID(publicid.KindDaemonRuntime, record.ID)
	if err != nil {
		return openapi.DaemonRuntime{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.DaemonRuntime{}, err
	}
	machineID, err := publicID(publicid.KindMachine, record.MachineID)
	if err != nil {
		return openapi.DaemonRuntime{}, err
	}
	capacity, err := jsonMapOrFallback(record.Capacity, json.RawMessage(`{}`))
	if err != nil {
		return openapi.DaemonRuntime{}, err
	}
	nextHeartbeatAfter := nextDaemonRuntimeHeartbeatAfter(record)
	return openapi.DaemonRuntime{
		Id:                   id,
		OrgId:                orgID,
		MachineId:            machineID,
		DaemonInstanceId:     record.DaemonInstanceID,
		DaemonVersion:        record.DaemonVersion,
		State:                openapi.DaemonRuntimeState(record.State),
		StateReasonCode:      record.StateReasonCode,
		StateReasonMessage:   record.StateReasonMessage,
		CreatedAt:            record.CreatedAt,
		LastSeenAt:           record.LastSeenAt,
		LeaseExpiresAt:       record.LeaseExpiresAt,
		NextHeartbeatAfterMs: int64(nextHeartbeatAfter / time.Millisecond),
		EndedAt:              nullableFromPtr(record.EndedAt),
		Capacity:             capacity,
	}, nil
}

func nextDaemonRuntimeHeartbeatAfter(record executionstore.DaemonRuntimeRecord) time.Duration {
	leaseHeartbeat := record.LeaseExpiresAt.Sub(record.LastSeenAt) / 3
	if leaseHeartbeat <= 0 || daemonRuntimeHeartbeatInterval < leaseHeartbeat {
		return daemonRuntimeHeartbeatInterval
	}
	return leaseHeartbeat
}

func daemonRuntimeReconciliationResponse(
	record executionstore.DaemonRuntimeReconciliation,
) (openapi.DaemonRuntimeReconciliation, error) {
	processes := make(
		[]openapi.ProcessReconciliationDirective,
		0,
		len(record.Processes),
	)
	for _, disposition := range record.Processes {
		processID, err := publicID(
			publicid.KindProcess,
			disposition.ProcessID,
		)
		if err != nil {
			return openapi.DaemonRuntimeReconciliation{}, err
		}
		actions := make(
			[]openapi.ProcessActionReconciliationDirective,
			0,
			len(disposition.Actions),
		)
		for _, action := range disposition.Actions {
			actionID, err := publicID(
				publicid.KindProcessAction,
				action.ProcessActionID,
			)
			if err != nil {
				return openapi.DaemonRuntimeReconciliation{}, err
			}
			var payload any
			if len(action.Payload) > 0 &&
				string(action.Payload) != "null" {
				if err := json.Unmarshal(action.Payload, &payload); err != nil {
					return openapi.DaemonRuntimeReconciliation{}, err
				}
			}
			actions = append(
				actions,
				openapi.ProcessActionReconciliationDirective{
					ProcessActionId: actionID,
					Seq:             action.Seq,
					ActionKind: openapi.ProcessActionReconciliationDirectiveActionKind(
						action.ActionKind,
					),
					Disposition: openapi.ProcessActionReconciliationDirectiveDisposition(
						action.Disposition,
					),
					Payload: payload,
				},
			)
		}
		processes = append(processes, openapi.ProcessReconciliationDirective{
			ProcessId:            processID,
			SupervisorInstanceId: disposition.SupervisorInstanceID,
			Disposition: openapi.ProcessReconciliationDirectiveDisposition(
				disposition.Disposition,
			),
			Actions: actions,
		})
	}
	return openapi.DaemonRuntimeReconciliation{Processes: processes}, nil
}

func publicIDs(kind publicid.Kind, ids []storage.ID) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		encoded, err := publicID(kind, id)
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	}
	return out, nil
}
