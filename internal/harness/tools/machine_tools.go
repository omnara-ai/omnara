package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrNoMachine = errors.New("no_machine")

type createMachineRequest struct {
	MachinePoolName string `json:"machine_pool_name"`
}

type machineIDRequest struct {
	MachineID string `json:"machine_id"`
}

type machineObservationMode string

const (
	machineObservationList    machineObservationMode = "list"
	machineObservationInspect machineObservationMode = "inspect"
)

type machineObservationAuthorization struct {
	Mode      machineObservationMode `json:"mode"`
	MachineID string                 `json:"machine_id,omitempty"`
}

func validateCreateMachineInput(input json.RawMessage) error {
	_, err := resolveCreateMachineRequest(input)
	return err
}

func validateDeleteMachineInput(input json.RawMessage) error {
	_, err := resolveMachineIDRequest(input, false)
	return err
}

func validateInspectMachineInput(input json.RawMessage) error {
	_, err := resolveMachineIDRequest(input, true)
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
	input, err := resolveMachineIDRequest(call.Call.Input, false)
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
	machineID, err := publicid.Decode(publicid.KindMachine, input.MachineID)
	if err != nil {
		return nil, err
	}
	command := executionstore.DeletePoolMachineForToolCall(
		executionstore.DeletePoolMachineInput{
			MachineID: machineID,
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
		payload, err := agentMachineObservation(machine)
		if err != nil {
			return nil, err
		}
		machineResults = append(machineResults, payload)
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
	input, err := resolveMachineIDRequest(call.Call.Input, true)
	if err != nil {
		return nil, err
	}
	var record executionstore.AgentMachineObservationRecord
	if input.MachineID != "" {
		machineID, decodeErr := publicid.Decode(publicid.KindMachine, input.MachineID)
		if decodeErr != nil {
			return nil, decodeErr
		}
		record, err = call.Reader.GetAgentMachineObservationByMachineID(ctx, machineID)
	} else {
		var machines []executionstore.AgentMachineObservationRecord
		machines, err = call.Reader.ListAgentMachineObservations(ctx)
		if err == nil {
			record, err = selectOnlyMachine(machines)
		}
	}
	if err != nil {
		if input.MachineID != "" && errors.Is(err, storeerr.ErrNotFound) {
			err = ErrMachineIDUnavailable
		}
		if errors.Is(err, ErrMachineSelectionRequired) ||
			errors.Is(err, ErrMachineIDUnavailable) {
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
	machineID, err := publicid.Encode(publicid.KindMachine, record.MachineID)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := machineObservationAuthorizationInput(
		machineObservationInspect,
		machineID,
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
	payload, err := agentMachineInspection(record)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(payload)
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
	machineID string,
) (json.RawMessage, error) {
	return marshalJSON(machineObservationAuthorization{
		Mode:      mode,
		MachineID: machineID,
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

func resolveOptionalMachineID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("parse machine_id: %w", err)
	}
	if value == nil {
		return "", errors.New("machine_id cannot be null")
	}
	id := strings.TrimSpace(*value)
	if id != "" {
		if _, err := publicid.Decode(publicid.KindMachine, id); err != nil {
			return "", errors.New("machine_id must be a valid public machine ID")
		}
	}
	return id, nil
}

func resolveMachineIDRequest(raw json.RawMessage, optional bool) (machineIDRequest, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return machineIDRequest{}, fmt.Errorf("parse machine request: %w", err)
	}
	for field := range body {
		if field != "machine_id" {
			return machineIDRequest{}, fmt.Errorf("machine request has unsupported field %q", field)
		}
	}
	machineID, err := resolveOptionalMachineID(body["machine_id"])
	if err != nil {
		return machineIDRequest{}, err
	}
	if machineID == "" && !optional {
		return machineIDRequest{}, errors.New("machine_id is required")
	}
	return machineIDRequest{MachineID: machineID}, nil
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
	MachineID              string    `json:"machine_id"`
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
	payload, err := machineObservation(record)
	if err != nil {
		return toolResultContent{}, err
	}
	return structuredToolResultContent(machineProvisioningAcceptedPayload{
		machineObservationPayload: payload,
		Created:                   true,
		Ready:                     false,
	})
}

func machineDeletionAcceptedResult(
	record executionstore.PoolMachineRecord,
) (toolResultContent, error) {
	payload, err := machineObservation(record)
	if err != nil {
		return toolResultContent{}, err
	}
	return structuredToolResultContent(machineDeletionAcceptedPayload{
		machineObservationPayload: payload,
		Deleted:                   false,
		DeletionInProgress:        true,
	})
}

func machineObservation(record executionstore.PoolMachineRecord) (machineObservationPayload, error) {
	machineID, err := publicid.Encode(publicid.KindMachine, record.Machine.ID)
	if err != nil {
		return machineObservationPayload{}, err
	}
	cwd := record.Binding.Cwd
	if cwd == "" {
		cwd = record.Machine.Cwd
	}
	return machineObservationPayload{
		MachineID:              machineID,
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
	}, nil
}

func agentMachineObservation(
	record executionstore.AgentMachineObservationRecord,
) (machineObservationPayload, error) {
	machineID, err := publicid.Encode(publicid.KindMachine, record.MachineID)
	if err != nil {
		return machineObservationPayload{}, err
	}
	payload := machineObservationPayload{
		MachineID:           machineID,
		BindingKind:         string(record.BindingKind),
		BindingState:        string(record.BindingState),
		Description:         record.Description,
		Executable:          record.Executable,
		ProjectGrantMissing: record.ProjectGrantMissing,
		CreatedAt:           record.BindingCreatedAt,
		UpdatedAt:           record.BindingUpdatedAt,
	}
	if record.ProjectGrantMissing {
		return payload, nil
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
	return payload, nil
}

func agentMachineInspection(
	record executionstore.AgentMachineObservationRecord,
) (machineInspectionPayload, error) {
	observation, err := agentMachineObservation(record)
	if err != nil {
		return machineInspectionPayload{}, err
	}
	payload := machineInspectionPayload{
		machineObservationPayload: observation,
	}
	if !record.ProjectGrantMissing {
		payload.FailureReport = record.FailureReport
	}
	return payload, nil
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
	content, err := toolFailureContent(code, message, retryable)
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

func toolFailureContent(
	code, message string,
	retryable bool,
) (toolResultContent, error) {
	return structuredToolResultContent(
		map[string]any{"error_code": code, "error": message, "message": message, "retryable": retryable},
	)
}
