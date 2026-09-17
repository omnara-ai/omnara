package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrNoMachine = errors.New("no_machine")

type createMachineRequest struct {
	MachinePoolID string `json:"machine_pool_id,omitempty"`
}

type listMachinesRequest struct {
	Cursor string `json:"cursor,omitempty"`
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
	var request listMachinesRequest
	if err := decodeSingleStrictJSON(input, &request, "list_machines request"); err != nil {
		return err
	}
	_, _, err := decodeMachineListCursor(request.Cursor)
	return err
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
	authorizationInput, err := machineCreateAuthorizationInput(source.MachinePoolID)
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
	machineID, err := resolveMachineIDRequest(call.Call.Input, false)
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		call.Call.Input,
	); err != nil {
		return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
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
	var input listMachinesRequest
	if err := decodeSingleStrictJSON(call.Call.Input, &input, "list_machines request"); err != nil {
		return nil, err
	}
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
	agentConfigID, err := agentConfigIDForModelContext(ctx, call.Reader, call.Turn.ModelCallContextID)
	if err != nil {
		return nil, err
	}
	pools, err := call.Reader.ListMachinePoolSources(ctx, agentConfigID)
	if err != nil {
		return nil, err
	}
	return machineListPage(machines, pools, input.Cursor)
}

// Pools come first, then machines, with stable ID ordering within each collection.
// The cursor's public ID kind identifies which collection to resume, even if the
// referenced resource has since been removed.
func decodeMachineListCursor(cursor string) (publicid.Kind, uuid.UUID, error) {
	if cursor == "" {
		return publicid.KindMachinePool, uuid.Nil, nil
	}
	for _, kind := range []publicid.Kind{publicid.KindMachinePool, publicid.KindMachine} {
		if id, err := publicid.Decode(kind, cursor); err == nil {
			return kind, id, nil
		}
	}
	return "", uuid.Nil, errors.New("cursor must be a valid machine_pool_id or machine_id from list_machines")
}

func machineListPage(
	machines []executionstore.AgentMachineObservationRecord,
	pools []executionstore.MachinePoolSourceRecord,
	cursor string,
) (transactionalPhaseResult, error) {
	kind, cursorID, err := decodeMachineListCursor(cursor)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(pools, func(a, b executionstore.MachinePoolSourceRecord) int {
		return bytes.Compare(a.MachinePoolID[:], b.MachinePoolID[:])
	})
	slices.SortFunc(machines, func(a, b executionstore.AgentMachineObservationRecord) int {
		return bytes.Compare(a.MachineID[:], b.MachineID[:])
	})
	page := machineListResult{Machines: []machineObservationPayload{}, MachinePools: []machinePoolPayload{}}
	entryBytes := 0
	reserveEntry := func(entry any, collectionSize int, nextCursor string) (bool, error) {
		encoded, err := marshalJSON(entry)
		if err != nil {
			return false, err
		}
		nextBytes := entryBytes + len(encoded)
		if collectionSize > 0 {
			nextBytes++ // comma within this collection
		}
		envelope, err := structuredToolResultContent(machineListResult{
			Machines: []machineObservationPayload{}, MachinePools: []machinePoolPayload{}, NextCursor: nextCursor,
		})
		if err != nil {
			return false, err
		}
		parts, err := envelope.contentParts()
		if err != nil {
			return false, err
		}
		if len(parts)+nextBytes > executionstore.ToolResultInlineBudgetBytes {
			return false, nil
		}
		entryBytes = nextBytes
		page.NextCursor = nextCursor
		return true, nil
	}
	finish := func() (transactionalPhaseResult, error) {
		content, err := structuredToolResultContent(page)
		if err != nil {
			return nil, err
		}
		return completeInTransaction(content), nil
	}
	if kind == publicid.KindMachinePool {
		for index, pool := range pools {
			if bytes.Compare(pool.MachinePoolID[:], cursorID[:]) <= 0 {
				continue
			}
			poolID, err := publicid.Encode(publicid.KindMachinePool, pool.MachinePoolID)
			if err != nil {
				return nil, err
			}
			entry := machinePoolPayload{
				MachinePoolID: poolID, MachinePoolName: pool.MachinePoolName, Description: pool.Description,
			}
			nextCursor := poolID
			if index == len(pools)-1 && len(machines) == 0 {
				nextCursor = ""
			}
			fits, err := reserveEntry(entry, len(page.MachinePools), nextCursor)
			if err != nil {
				return nil, err
			}
			if !fits {
				if len(page.MachinePools) == 0 {
					return failMachineTransaction("machine_pool_details_too_large", fmt.Errorf(
						"details for machine pool %s exceed the list_machines size limit; call list_machines with cursor %q to continue",
						poolID, poolID,
					), false)
				}
				return finish()
			}
			page.MachinePools = append(page.MachinePools, entry)
		}
	}
	for index, machine := range machines {
		if kind == publicid.KindMachine && bytes.Compare(machine.MachineID[:], cursorID[:]) <= 0 {
			continue
		}
		observation, err := agentMachineObservation(machine)
		if err != nil {
			return nil, err
		}
		nextCursor := ""
		if index < len(machines)-1 {
			nextCursor = observation.MachineID
		}
		fits, err := reserveEntry(observation, len(page.Machines), nextCursor)
		if err != nil {
			return nil, err
		}
		if !fits {
			if len(page.Machines)+len(page.MachinePools) == 0 {
				return failMachineTransaction("machine_details_too_large", fmt.Errorf(
					"details for machine %s exceed the list_machines size limit; use inspect_machine for details, or call list_machines with cursor %q to continue",
					observation.MachineID, observation.MachineID,
				), false)
			}
			break
		}
		page.Machines = append(page.Machines, observation)
	}
	return finish()
}

func inspectMachine(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	machineID, err := resolveMachineIDRequest(call.Call.Input, true)
	if err != nil {
		return nil, err
	}
	var record executionstore.AgentMachineObservationRecord
	if machineID != uuid.Nil {
		record, err = call.Reader.GetAgentMachineObservationByMachineID(ctx, machineID)
	} else {
		var machines []executionstore.AgentMachineObservationRecord
		machines, err = call.Reader.ListAgentMachineObservations(ctx)
		if err == nil {
			record, err = selectOnlyMachine(machines)
		}
	}
	if err != nil {
		if machineID != uuid.Nil && errors.Is(err, storeerr.ErrNotFound) {
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
	machinePublicID, err := publicid.Encode(publicid.KindMachine, record.MachineID)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := machineObservationAuthorizationInput(
		machineObservationInspect,
		machinePublicID,
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
	toolCallID uuid.UUID,
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
	toolCallID uuid.UUID,
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
		if field != "machine_pool_id" {
			return createMachineRequest{}, fmt.Errorf("create_machine request has unsupported field %q", field)
		}
		if string(value) == "null" {
			return createMachineRequest{}, errors.New("create_machine machine_pool_id cannot be null")
		}
		if _, err := publicid.Decode(publicid.KindMachinePool, input.MachinePoolID); err != nil {
			return createMachineRequest{}, fmt.Errorf("machine_pool_id must be a valid public machine pool ID: %w", err)
		}
	}
	return input, nil
}

func agentConfigIDForModelContext(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	modelCallContextID uuid.UUID,
) (uuid.UUID, error) {
	contextRecord, found, err := reader.GetModelCallContext(ctx, modelCallContextID)
	if err != nil {
		return uuid.Nil, err
	}
	if !found {
		return uuid.Nil, fmt.Errorf("model call context not found: %w", storeerr.ErrNotFound)
	}
	return contextRecord.AgentConfigID, nil
}

func machineCreateAuthorizationInput(
	machinePoolID uuid.UUID,
) (json.RawMessage, error) {
	poolID, err := publicid.Encode(publicid.KindMachinePool, machinePoolID)
	if err != nil {
		return nil, err
	}
	return marshalJSON(createMachineRequest{MachinePoolID: poolID})
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
	if input.MachinePoolID != "" {
		poolID, err := publicid.Decode(publicid.KindMachinePool, input.MachinePoolID)
		if err != nil {
			return executionstore.MachinePoolSourceRecord{}, err
		}
		for _, source := range sources {
			if source.MachinePoolID == poolID {
				return source, nil
			}
		}
		return executionstore.MachinePoolSourceRecord{}, fmt.Errorf(
			"machine_pool_id is not available to this agent: %w",
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
			"machine_pool_id is required when multiple machine pools are available; use list_machines to discover pools",
		)
	}
}

func resolveOptionalMachineID(raw json.RawMessage) (uuid.UUID, error) {
	if len(raw) == 0 {
		return uuid.Nil, nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil, fmt.Errorf("parse machine_id: %w", err)
	}
	if value == nil {
		return uuid.Nil, errors.New("machine_id cannot be null")
	}
	id, err := publicid.Decode(publicid.KindMachine, *value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("machine_id must be a valid public machine ID: %w", err)
	}
	return id, nil
}

func resolveMachineIDRequest(raw json.RawMessage, optional bool) (uuid.UUID, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return uuid.Nil, fmt.Errorf("parse machine request: %w", err)
	}
	for field := range body {
		if field != "machine_id" {
			return uuid.Nil, fmt.Errorf("machine request has unsupported field %q", field)
		}
	}
	machineID, err := resolveOptionalMachineID(body["machine_id"])
	if err != nil {
		return uuid.Nil, err
	}
	if machineID == uuid.Nil && !optional {
		return uuid.Nil, errors.New("machine_id is required")
	}
	return machineID, nil
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
	MachinePoolID          string    `json:"machine_pool_id,omitempty"`
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
	Machines     []machineObservationPayload `json:"machines"`
	MachinePools []machinePoolPayload        `json:"machine_pools"`
	NextCursor   string                      `json:"next_cursor,omitempty"`
}

type machinePoolPayload struct {
	MachinePoolID   string `json:"machine_pool_id"`
	MachinePoolName string `json:"machine_pool_name"`
	Description     string `json:"description,omitempty"`
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
	var poolID string
	if record.Machine.MachinePoolID != uuid.Nil {
		poolID, err = publicid.Encode(publicid.KindMachinePool, record.Machine.MachinePoolID)
		if err != nil {
			return machineObservationPayload{}, err
		}
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
		MachinePoolID:          poolID,
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
	if record.MachinePoolID != uuid.Nil {
		payload.MachinePoolID, err = publicid.Encode(publicid.KindMachinePool, record.MachinePoolID)
		if err != nil {
			return machineObservationPayload{}, err
		}
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
