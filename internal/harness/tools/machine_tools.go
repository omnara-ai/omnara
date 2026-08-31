package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrNoMachine = errors.New("no_machine")

type createMachineRequest struct {
	MachinePoolName string `json:"machine_pool_name"`
}

type machineRefRequest struct {
	MachineRef string `json:"machine_ref"`
}

type machineObservationMode string

const (
	machineObservationList    machineObservationMode = "list"
	machineObservationInspect machineObservationMode = "inspect"
)

type machineObservationAuthorization struct {
	Mode       machineObservationMode `json:"mode"`
	MachineRef string                 `json:"machine_ref,omitempty"`
}

func validateCreateMachineInput(input json.RawMessage) error {
	_, err := resolveCreateMachineRequest(input)
	return err
}

func validateDeleteMachineInput(input json.RawMessage) error {
	_, err := resolveMachineRefRequest(input, false)
	return err
}

func validateInspectMachineInput(input json.RawMessage) error {
	_, err := resolveMachineRefRequest(input, true)
	return err
}

func validateListMachinesInput(input json.RawMessage) error {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(input, &body); err != nil {
		return fmt.Errorf("parse list_machines request: %w", err)
	}
	if len(body) != 0 {
		return errors.New("list_machines request has unsupported fields")
	}
	return nil
}

func createMachine(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	input, err := resolveCreateMachineRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	agentConfigID, err := agentConfigIDForModelContext(
		ctx,
		call.Reader,
		call.Turn.ModelCallContextID,
	)
	if err != nil {
		return nil, err
	}
	sources, err := call.Reader.ListMachinePoolSources(ctx, agentConfigID)
	if err != nil {
		return nil, err
	}
	source, err := selectPoolForMachineCreate(sources, input)
	if err != nil {
		return failMachineTransaction("create_machine_failed", err, false)
	}
	authorizationInput, err := machineCreateAuthorizationInput(source.MachinePoolName)
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
	}
	command := executionstore.CreatePoolMachineForToolCall(
		executionstore.CreatePoolMachineInput{
			MachinePoolID: source.MachinePoolID,
		},
		func(created executionstore.CreatePoolMachineResult) (executionstore.ToolCallCompletionInput, error) {
			content, err := machineProvisioningAcceptedResult(created.Machine)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(
		command,
		func(err error) (transactionalPhaseResult, error) {
			return failMachineTransactionForStorageError(
				"create_machine_failed",
				err,
				false,
			)
		},
	), nil
}

func deleteMachine(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	input, err := resolveMachineRefRequest(call.Call.Input, false)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := marshalJSON(input)
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
	}
	command := executionstore.DeletePoolMachineForToolCall(
		executionstore.DeletePoolMachineInput{
			MachineRef: input.MachineRef,
		},
		func(record executionstore.PoolMachineRecord) (executionstore.ToolCallCompletionInput, error) {
			content, err := machineDeletionAcceptedResult(record)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(
		command,
		func(err error) (transactionalPhaseResult, error) {
			retryable := errors.Is(err, storeerr.ErrStateTransitionConflict)
			return failMachineTransactionForStorageError(
				"delete_machine_failed",
				err,
				retryable,
			)
		},
	), nil
}

func listMachines(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	authorizationInput, err := machineObservationAuthorizationInput(machineObservationList, "")
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, err
	}
	machines, err := call.Reader.ListAgentMachineObservations(ctx)
	if err != nil {
		return nil, err
	}
	machineResults := make([]machineObservationPayload, 0, len(machines))
	for _, machine := range machines {
		machineResults = append(machineResults, agentMachineObservation(machine))
	}
	content, err := structuredToolResultContent(
		machineListResult{Machines: machineResults},
	)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func inspectMachine(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	input, err := resolveMachineRefRequest(call.Call.Input, true)
	if err != nil {
		return nil, err
	}
	var record executionstore.AgentMachineObservationRecord
	if input.MachineRef != "" {
		record, err = call.Reader.GetAgentMachineObservationByRef(ctx, input.MachineRef)
	} else {
		var machines []executionstore.AgentMachineObservationRecord
		machines, err = call.Reader.ListAgentMachineObservations(ctx)
		if err == nil {
			record, err = selectOnlyMachine(machines)
		}
	}
	if err != nil {
		if input.MachineRef != "" && errors.Is(err, storeerr.ErrNotFound) {
			err = ErrMachineRefUnavailable
		}
		if errors.Is(err, ErrMachineSelectionRequired) ||
			errors.Is(err, ErrMachineRefUnavailable) {
			unavailable, resultErr := machineUnavailableToolResult(err)
			if resultErr != nil {
				return nil, resultErr
			}
			return failInTransaction(unavailable.Content, unavailable.Cause), nil
		}
		if errors.Is(err, ErrNoMachine) {
			return failMachineTransactionWithMessage(
				"inspect_machine_failed",
				"no machines are associated with this agent",
				err,
				false,
			)
		}
		if !errors.Is(err, storeerr.ErrNotFound) {
			return nil, err
		}
		return failMachineTransaction("inspect_machine_failed", err, false)
	}
	authorizationInput, err := machineObservationAuthorizationInput(
		machineObservationInspect,
		record.MachineRef,
	)
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(agentMachineInspection(record))
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func provisionMachineInBackground(
	ctx context.Context,
	call backgroundToolContext,
) error {
	return call.Executor.provisionMachineForToolCall(ctx, call.Turn, call.ToolCallID)
}

func deleteMachineInBackground(
	ctx context.Context,
	call backgroundToolContext,
) error {
	return call.Executor.deleteMachineForToolCall(ctx, call.Turn, call.ToolCallID)
}

func (e Executor) provisionMachineForToolCall(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
) error {
	record, err := e.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		toolCallID,
	)
	if err != nil {
		return err
	}
	if e.MachinePoolManager == nil {
		return errors.New("pool machine manager is required")
	}
	attemptCtx, cancel := context.WithTimeout(ctx, machinepool.DefaultImmediateProvisioningTimeout)
	defer cancel()
	return e.MachinePoolManager.ProvisionMachine(
		attemptCtx,
		record.Machine.OrgID,
		record.Machine.ID,
	)
}

func (e Executor) deleteMachineForToolCall(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
) error {
	record, err := e.Store.Execution().GetPoolMachineByDeleteToolCall(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		toolCallID,
	)
	if err != nil {
		return err
	}
	if e.MachinePoolManager == nil {
		return errors.New("pool machine manager is required")
	}
	attemptCtx, cancel := context.WithTimeout(ctx, machinepool.DefaultImmediateDeletionTimeout)
	defer cancel()
	return e.MachinePoolManager.DeleteMachine(
		attemptCtx,
		executionstore.PoolMachineCleanupCandidate{
			Machine:       record.Machine,
			ReasonCode:    record.Machine.LifecycleReasonCode,
			ReasonMessage: record.Machine.LifecycleReasonMessage,
		},
	)
}

func resolveCreateMachineRequest(raw json.RawMessage) (createMachineRequest, error) {
	var input createMachineRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		return createMachineRequest{}, fmt.Errorf("parse create_machine request: %w", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return createMachineRequest{}, fmt.Errorf("parse create_machine request: %w", err)
	}
	for field, value := range body {
		if field != "machine_pool_name" {
			return createMachineRequest{}, fmt.Errorf("create_machine request has unsupported field %q", field)
		}
		if string(value) == "null" {
			return createMachineRequest{}, errors.New("create_machine machine_pool_name cannot be null")
		}
	}
	return input, nil
}

func agentConfigIDForModelContext(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	modelCallContextID storage.ID,
) (storage.ID, error) {
	contextRecord, found, err := reader.GetModelCallContext(ctx, modelCallContextID)
	if err != nil {
		return storage.NilID, err
	}
	if !found {
		return storage.NilID, fmt.Errorf("model call context not found: %w", storeerr.ErrNotFound)
	}
	return contextRecord.AgentConfigID, nil
}

func machineCreateAuthorizationInput(
	machinePoolName string,
) (json.RawMessage, error) {
	return marshalJSON(createMachineRequest{MachinePoolName: machinePoolName})
}

func machineObservationAuthorizationInput(
	mode machineObservationMode,
	machineRef string,
) (json.RawMessage, error) {
	return marshalJSON(machineObservationAuthorization{
		Mode:       mode,
		MachineRef: machineRef,
	})
}

func selectPoolForMachineCreate(
	sources []executionstore.MachinePoolSourceRecord,
	input createMachineRequest,
) (executionstore.MachinePoolSourceRecord, error) {
	if input.MachinePoolName != "" {
		for _, source := range sources {
			if source.MachinePoolName == input.MachinePoolName {
				return source, nil
			}
		}
		return executionstore.MachinePoolSourceRecord{}, fmt.Errorf(
			"machine_pool_name is not configured for this agent: %w",
			storeerr.ErrNotFound,
		)
	}
	switch len(sources) {
	case 0:
		return executionstore.MachinePoolSourceRecord{}, fmt.Errorf(
			"no machine pools are configured: %w",
			storeerr.ErrNotFound,
		)
	case 1:
		return sources[0], nil
	default:
		return executionstore.MachinePoolSourceRecord{}, errors.New(
			"machine_pool_name is required when multiple machine pools are available",
		)
	}
}

func resolveMachineRefRequest(raw json.RawMessage, optional bool) (machineRefRequest, error) {
	var input machineRefRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		return machineRefRequest{}, fmt.Errorf("parse machine request: %w", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return machineRefRequest{}, fmt.Errorf("parse machine request: %w", err)
	}
	for field, value := range body {
		if field != "machine_ref" {
			return machineRefRequest{}, fmt.Errorf("machine request has unsupported field %q", field)
		}
		if string(value) == "null" {
			return machineRefRequest{}, errors.New("machine machine_ref cannot be null")
		}
	}
	if input.MachineRef == "" && !optional {
		return machineRefRequest{}, errors.New("machine_ref is required")
	}
	return input, nil
}

func selectOnlyMachine[T any](machines []T) (T, error) {
	var zero T
	switch len(machines) {
	case 0:
		return zero, ErrNoMachine
	case 1:
		return machines[0], nil
	default:
		return zero, ErrMachineSelectionRequired
	}
}

type machineObservationPayload struct {
	MachineRef             string    `json:"machine_ref"`
	SourceKind             string    `json:"source_kind,omitempty"`
	BindingKind            string    `json:"binding_kind,omitempty"`
	BindingState           string    `json:"binding_state"`
	DisplayName            string    `json:"display_name,omitempty"`
	MachinePoolName        string    `json:"machine_pool_name,omitempty"`
	LifecycleState         string    `json:"lifecycle_state"`
	ConnectionState        string    `json:"connection_state"`
	ConnectionStateReason  string    `json:"connection_state_reason,omitempty"`
	Description            string    `json:"description"`
	Cwd                    string    `json:"cwd"`
	Executable             bool      `json:"executable"`
	ProjectGrantMissing    bool      `json:"project_grant_missing,omitempty"`
	LifecycleReasonCode    string    `json:"lifecycle_reason_code"`
	LifecycleReasonMessage string    `json:"lifecycle_reason_message"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type machineListResult struct {
	Machines []machineObservationPayload `json:"machines"`
}

type machineInspectionPayload struct {
	machineObservationPayload
	FailureReport json.RawMessage `json:"failure_report,omitempty"`
}

type machineProvisioningAcceptedPayload struct {
	machineObservationPayload
	Created bool `json:"created"`
	Ready   bool `json:"ready"`
}

type machineDeletionAcceptedPayload struct {
	machineObservationPayload
	Deleted            bool `json:"deleted"`
	DeletionInProgress bool `json:"deletion_in_progress"`
}

func machineProvisioningAcceptedResult(
	record executionstore.PoolMachineRecord,
) (toolResultContent, error) {
	return structuredToolResultContent(machineProvisioningAcceptedPayload{
		machineObservationPayload: machineObservation(record),
		Created:                   true,
		Ready:                     false,
	})
}

func machineDeletionAcceptedResult(
	record executionstore.PoolMachineRecord,
) (toolResultContent, error) {
	return structuredToolResultContent(machineDeletionAcceptedPayload{
		machineObservationPayload: machineObservation(record),
		Deleted:                   false,
		DeletionInProgress:        true,
	})
}

func machineObservation(record executionstore.PoolMachineRecord) machineObservationPayload {
	cwd := record.Binding.Cwd
	if cwd == "" {
		cwd = record.Machine.Cwd
	}
	return machineObservationPayload{
		MachineRef:             record.Binding.MachineRef,
		SourceKind:             string(record.Machine.SourceKind),
		BindingKind:            string(record.Binding.BindingKind),
		BindingState:           string(record.Binding.State),
		DisplayName:            record.Machine.DisplayName,
		MachinePoolName:        record.MachinePoolName,
		LifecycleState:         string(record.Machine.LifecycleState),
		ConnectionState:        string(record.Machine.ConnectionState),
		ConnectionStateReason:  record.Machine.ConnectionStateReason,
		Description:            record.Binding.Description,
		Cwd:                    cwd,
		Executable:             machineExecutable(record),
		LifecycleReasonCode:    record.Machine.LifecycleReasonCode,
		LifecycleReasonMessage: record.Machine.LifecycleReasonMessage,
		CreatedAt:              record.Binding.CreatedAt,
		UpdatedAt:              record.Binding.UpdatedAt,
	}
}

func agentMachineObservation(
	record executionstore.AgentMachineObservationRecord,
) machineObservationPayload {
	payload := machineObservationPayload{
		MachineRef:          record.MachineRef,
		BindingKind:         string(record.BindingKind),
		BindingState:        string(record.BindingState),
		Description:         record.Description,
		Executable:          record.Executable,
		ProjectGrantMissing: record.ProjectGrantMissing,
		CreatedAt:           record.BindingCreatedAt,
		UpdatedAt:           record.BindingUpdatedAt,
	}
	if record.ProjectGrantMissing {
		return payload
	}
	payload.SourceKind = string(record.SourceKind)
	payload.DisplayName = record.DisplayName
	payload.MachinePoolName = record.MachinePoolName
	payload.LifecycleState = string(record.LifecycleState)
	payload.ConnectionState = string(record.ConnectionState)
	payload.ConnectionStateReason = record.ConnectionStateReason
	payload.Cwd = record.Cwd
	payload.LifecycleReasonCode = record.LifecycleReasonCode
	payload.LifecycleReasonMessage = record.LifecycleReasonMessage
	return payload
}

func agentMachineInspection(
	record executionstore.AgentMachineObservationRecord,
) machineInspectionPayload {
	payload := machineInspectionPayload{
		machineObservationPayload: agentMachineObservation(record),
	}
	if !record.ProjectGrantMissing {
		payload.FailureReport = record.FailureReport
	}
	return payload
}

func machineExecutable(record executionstore.PoolMachineRecord) bool {
	return record.Binding.State == executionstore.AgentMachineBindingStateAttached &&
		record.Machine.LifecycleState == executionstore.MachineLifecycleStateActive &&
		(record.Machine.ConnectionState == executionstore.MachineConnectionStateOnline ||
			record.Machine.ConnectionState == executionstore.MachineConnectionStateAsleep)
}

func failMachineTransaction(
	code string,
	cause error,
	retryable bool,
) (transactionalPhaseResult, error) {
	return failMachineTransactionWithMessage(code, cause.Error(), cause, retryable)
}

func failMachineTransactionWithMessage(
	code, message string,
	cause error,
	retryable bool,
) (transactionalPhaseResult, error) {
	content, err := machineToolFailureContent(code, message, retryable)
	if err != nil {
		return nil, fmt.Errorf("marshal machine tool failure: %w", err)
	}
	return failInTransaction(content, cause), nil
}

func failMachineTransactionForStorageError(
	code string,
	cause error,
	retryable bool,
) (transactionalPhaseResult, error) {
	if errors.Is(cause, storeerr.ErrManagedWorkAdmissionDenied) {
		return failMachineTransactionWithMessage(
			storeerr.ManagedWorkAdmissionDeniedCode,
			storeerr.InsufficientOmnaraCreditsMessage,
			cause,
			false,
		)
	}
	if !errors.Is(cause, storeerr.ErrInvalidRequest) &&
		!errors.Is(cause, storeerr.ErrNotFound) &&
		!errors.Is(cause, storeerr.ErrStateTransitionConflict) {
		return nil, cause
	}
	return failMachineTransaction(code, cause, retryable)
}

func machineToolFailureContent(
	code, message string,
	retryable bool,
) (toolResultContent, error) {
	return structuredToolResultContent(
		map[string]any{"error_code": code, "error": message, "message": message, "retryable": retryable},
	)
}
