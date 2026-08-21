package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type writeProcessRequest struct {
	ProcessID  string  `json:"process_id"`
	Data       *string `json:"data,omitempty"`
	CloseStdin *bool   `json:"close_stdin,omitempty"`
}

type stopProcessRequest struct {
	ProcessID string `json:"process_id"`
	Mode      string `json:"mode"`
}

type runCommandAuthorization struct {
	AgentMachineBindingID string                   `json:"agent_machine_binding_id"`
	Command               string                   `json:"command"`
	ShellSelector         processcmd.ShellSelector `json:"shell_selector"`
	Cwd                   string                   `json:"cwd,omitempty"`
	WaitMS                int                      `json:"wait_ms,omitempty"`
	IOMode                processcmd.IOMode        `json:"io_mode"`
}

type processActionAuthorization struct {
	ProcessID  string             `json:"process_id"`
	ActionKind processaction.Kind `json:"action_kind"`
	Payload    json.RawMessage    `json:"payload"`
}

type processObservationMode string

const (
	processObservationRead processObservationMode = "read"
	processObservationList processObservationMode = "list"
)

type processObservationRequest struct {
	ProcessID string                 `json:"process_id,omitempty"`
	Mode      processObservationMode `json:"mode"`
	Cursor    *int64                 `json:"cursor,omitempty"`
	MaxBytes  int                    `json:"max_bytes,omitempty"`
	WaitMS    int                    `json:"wait_ms,omitempty"`
}

func runCommandAuthorizationInput(
	bindingID storage.ID,
	resolved resolvedRunCommandRequest,
) (json.RawMessage, error) {
	request, err := marshalJSON(runCommandAuthorization{
		AgentMachineBindingID: bindingID.String(),
		Command:               resolved.Command,
		ShellSelector:         resolved.Selector,
		Cwd:                   resolved.Cwd,
		WaitMS:                resolved.WaitMs,
		IOMode:                resolved.IOMode,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal run_command authorization: %w", err)
	}
	return request, nil
}

func startCommand(
	ctx context.Context,
	call transactionalToolContext,
	binding executionstore.AgentMachineBindingRecord,
	resolved resolvedRunCommandRequest,
) (transactionalPhaseResult, error) {
	authorizationInput, err := runCommandAuthorizationInput(binding.ID, resolved)
	if err != nil {
		return nil, err
	}
	err = authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	)
	if err != nil {
		return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
	}
	command := executionstore.StartProcessForToolCall(executionstore.CreateProcessInput{
		AgentMachineBindingID: binding.ID,
		IOMode:                resolved.IOMode,
		Command:               resolved.Command,
		ShellSelector:         resolved.Selector,
		Cwd:                   resolved.Cwd,
		TimeoutSeconds:        0,
		InitialWaitMS:         resolved.WaitMs,
	})
	return executeInTransaction(
		command,
		func(err error) (transactionalPhaseResult, error) {
			if errors.Is(err, storeerr.ErrManagedWorkAdmissionDenied) {
				return failProcessTransaction(
					err,
					processToolErrorManagedWorkAdmissionDenied,
					managedWorkAdmissionDeniedMessage,
					false,
					"",
					"",
					"",
				)
			}
			if errors.Is(err, storeerr.ErrMachineNotReachable) {
				return failProcessTransaction(
					err,
					processToolErrorStartUnavailable,
					"machine is offline; try again when online",
					true,
					"",
					"",
					"",
				)
			}
			if errors.Is(err, storeerr.ErrAgentProcessLimitReached) {
				return failProcessTransaction(
					err,
					processToolErrorAgentLimitReached,
					"this agent has reached its unfinished process limit; use list_processes and stop_process before starting another",
					false,
					"",
					"",
					processToolNextActionList,
				)
			}
			return nil, err
		},
	), nil
}

func runCommand(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveRunCommandRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, resolved.MachineRef)
	if err != nil {
		return processToolMachineResolutionError(err)
	}
	return startCommand(ctx, call, binding, resolved)
}

func wakeRunCommand(
	ctx context.Context,
	call backgroundToolContext,
) error {
	process, found, err := call.Executor.Store.Execution().GetProcessByToolCall(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("run_command process was not created")
	}
	err = wakeProcessMachine(ctx, call, process)
	if err != nil {
		_, failErr := call.Executor.Store.Execution().FailQueuedProcessAfterWakeFailure(ctx, process)
		return errors.Join(err, failErr)
	}
	return nil
}

func wakeReadProcess(
	ctx context.Context,
	call backgroundToolContext,
) error {
	action, found, err := call.Executor.Store.Execution().GetProcessActionByToolCall(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("read_process action was not created")
	}
	process, err := call.Executor.Store.Execution().GetProcess(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		action.ProcessID,
	)
	if err != nil {
		return err
	}
	err = wakeProcessMachine(ctx, call, process)
	if err == nil {
		return nil
	}
	_, failErr := call.Executor.Store.Execution().FailQueuedProcessActionsAfterWakeFailure(
		ctx,
		process,
		action,
	)
	return errors.Join(err, failErr)
}

func wakeProcessMachine(
	ctx context.Context,
	call backgroundToolContext,
	process executionstore.ProcessRecord,
) error {
	if call.Executor.MachinePoolManager == nil {
		return errors.New("machine pool manager is not configured")
	}
	shouldWait, err := call.Executor.MachinePoolManager.WakeMachine(ctx, process.OrgID, process.MachineID)
	if err != nil || shouldWait {
		return err
	}
	return storeerr.ErrMachineNotReachable
}

func processToolMachineResolutionError(
	err error,
) (transactionalPhaseResult, error) {
	if !errors.Is(err, ErrNoActiveAgentMachineBinding) &&
		!errors.Is(err, ErrMachineRefUnavailable) &&
		!errors.Is(err, ErrMachineSelectionRequired) {
		return nil, err
	}
	unavailable, resultErr := machineUnavailableToolResult(err)
	if resultErr != nil {
		return nil, resultErr
	}
	return failInTransaction(unavailable.Content, unavailable.Cause), nil
}

func validateRunCommandInput(input json.RawMessage) error {
	_, err := resolveRunCommandRequest(input)
	return err
}

func writeProcess(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	processID, payload, err := writeProcessPayload(call.Call.Input)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := processActionAuthorizationRequest(
		processID,
		executionstore.ProcessActionKindWrite,
		payload,
	)
	if err != nil {
		return nil, err
	}
	return createProcessAction(
		ctx,
		call,
		processID,
		executionstore.ProcessActionKindWrite,
		payload,
		authorizationInput,
		executionstore.CreateProcessActionForToolCall,
	)
}

func stopProcess(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	processID, kind, err := resolveStopProcessRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := processActionAuthorizationRequest(
		processID,
		kind,
		json.RawMessage(`{}`),
	)
	if err != nil {
		return nil, err
	}
	alreadyStopped, err := alreadyStoppedProcessResult(processID, kind)
	if err != nil {
		return nil, err
	}
	alreadyStoppedCompletion, err := successfulToolCallCompletion(alreadyStopped)
	if err != nil {
		return nil, err
	}
	return createProcessAction(
		ctx,
		call,
		processID,
		kind,
		json.RawMessage(`{}`),
		authorizationInput,
		func(input executionstore.CreateProcessActionInput) executionstore.ToolCallCommand {
			return executionstore.StopProcessForToolCall(input, alreadyStoppedCompletion)
		},
	)
}

func alreadyStoppedProcessResult(
	processID string,
	kind executionstore.ProcessActionKind,
) (toolResultContent, error) {
	return structuredToolResultContent(struct {
		ProcessID       string                            `json:"process_id"`
		Mode            processaction.Kind                `json:"mode"`
		State           executionstore.ProcessActionState `json:"state"`
		StateReasonCode string                            `json:"state_reason_code"`
	}{
		ProcessID:       processID,
		Mode:            kind,
		State:           executionstore.ProcessActionStateApplied,
		StateReasonCode: daemonprotocol.ProcessActionReasonAlreadyStopped,
	})
}

func observeProcess(
	ctx context.Context,
	call transactionalToolContext,
	mode processObservationMode,
) (transactionalPhaseResult, error) {
	request, err := resolveProcessObservationRequest(call.Call.Input, mode)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := marshalJSON(request)
	if err != nil {
		return nil, fmt.Errorf("marshal %s_process authorization: %w", mode, err)
	}
	if mode == processObservationList {
		if err := authorizeToolExecution(
			ctx,
			call.Reader,
			call.Turn,
			call.Call,
			authorizationInput,
		); err != nil {
			return nil, err
		}
		records, err := call.Reader.ListActiveProcesses(ctx)
		if err != nil {
			return nil, err
		}
		content, err := structuredToolResultContent(struct {
			Processes []processListObservation `json:"processes"`
		}{Processes: processListObservations(records)})
		if err != nil {
			return nil, fmt.Errorf("marshal process list result: %w", err)
		}
		return completeInTransaction(content), nil
	}
	payload, err := processObservationActionPayload(request)
	if err != nil {
		return nil, fmt.Errorf("marshal process observation payload: %w", err)
	}
	return createProcessAction(
		ctx,
		call,
		request.ProcessID,
		processaction.Kind(mode),
		payload,
		authorizationInput,
		executionstore.CreateProcessActionForToolCall,
	)
}

func processObservationActionPayload(
	request processObservationRequest,
) (json.RawMessage, error) {
	return marshalJSON(processaction.ReadPayload{
		Cursor:   request.Cursor,
		MaxBytes: request.MaxBytes,
		WaitMS:   request.WaitMS,
	})
}

func processActionAuthorizationRequest(
	processID string,
	kind executionstore.ProcessActionKind,
	payload json.RawMessage,
) (json.RawMessage, error) {
	request, err := marshalJSON(processActionAuthorization{
		ProcessID:  processID,
		ActionKind: kind,
		Payload:    payload,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal process action authorization: %w", err)
	}
	return request, nil
}

func createProcessAction(
	ctx context.Context,
	call transactionalToolContext,
	processID string,
	kind executionstore.ProcessActionKind,
	payload json.RawMessage,
	authorizationInput json.RawMessage,
	commandFactory func(executionstore.CreateProcessActionInput) executionstore.ToolCallCommand,
) (transactionalPhaseResult, error) {
	err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	)
	if err != nil {
		return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
	}
	parsedProcessID, err := decodeProcessID(processID)
	if err != nil {
		return failProcessTransaction(
			err,
			processToolErrorInvalidID,
			"process_id is invalid",
			false,
			processID,
			"",
			processToolNextActionList,
		)
	}
	input := executionstore.CreateProcessActionInput{
		ProcessID:  parsedProcessID,
		ActionKind: kind,
		Payload:    payload,
	}
	return executeInTransaction(
		commandFactory(input),
		func(err error) (transactionalPhaseResult, error) {
			if errors.Is(err, storeerr.ErrProcessStateUnknown) {
				return failProcessTransaction(
					err,
					processToolErrorStateUnknown,
					"the process may still exist, so stopping it cannot be confirmed",
					false,
					processID,
					executionstore.ProcessStateUnknown,
					"",
				)
			}
			if errors.Is(err, storeerr.ErrProcessTerminal) {
				return failProcessTransaction(
					err,
					processToolErrorTerminal,
					"process is already terminal",
					false,
					processID,
					"",
					"",
				)
			}
			if errors.Is(err, storeerr.ErrProcessTerminating) {
				return failProcessTransaction(
					err,
					processToolErrorTerminating,
					"process is terminating; new mutating actions are not accepted",
					false,
					processID,
					"",
					"",
				)
			}
			if errors.Is(err, storeerr.ErrNoOnlineDaemonRuntime) {
				return failProcessTransaction(
					err,
					processToolErrorActionUnavailable,
					"machine is offline; try again when online",
					true,
					processID,
					"",
					"",
				)
			}
			if errors.Is(err, storeerr.ErrNotFound) {
				return failProcessTransaction(
					err,
					processToolErrorNotFound,
					"process was not found",
					false,
					processID,
					"",
					processToolNextActionList,
				)
			}
			return nil, fmt.Errorf(
				"create process action %s for process %s: %w",
				kind,
				processID,
				err,
			)
		},
	), nil
}

func processObservation(mode processObservationMode) transactionalToolHandler {
	return func(
		ctx context.Context,
		call transactionalToolContext,
	) (transactionalPhaseResult, error) {
		return observeProcess(ctx, call, mode)
	}
}

func resolveStopProcessRequest(
	raw json.RawMessage,
) (string, executionstore.ProcessActionKind, error) {
	var input stopProcessRequest
	if err := decodeSingleStrictJSON(raw, &input, "stop_process request"); err != nil {
		return "", "", fmt.Errorf("parse stop_process request: %w", err)
	}
	if input.ProcessID == "" {
		return "", "", errors.New("stop_process requires process_id")
	}
	switch input.Mode {
	case string(executionstore.ProcessActionKindInterrupt):
		return input.ProcessID, executionstore.ProcessActionKindInterrupt, nil
	case string(executionstore.ProcessActionKindTerminate):
		return input.ProcessID, executionstore.ProcessActionKindTerminate, nil
	default:
		return "", "", errors.New("stop_process mode must be interrupt or terminate")
	}
}

type processToolErrorCode string

const (
	processToolErrorStartUnavailable           processToolErrorCode = "process_start_unavailable"
	processToolErrorAgentLimitReached          processToolErrorCode = "agent_process_limit_reached"
	processToolErrorManagedWorkAdmissionDenied processToolErrorCode = storeerr.ManagedWorkAdmissionDeniedCode
	processToolErrorStateUnknown               processToolErrorCode = "process_state_unknown"
	processToolErrorInvalidID                  processToolErrorCode = "invalid_process_id"
	processToolErrorTerminal                   processToolErrorCode = "process_terminal"
	processToolErrorTerminating                processToolErrorCode = "process_terminating"
	processToolErrorActionUnavailable          processToolErrorCode = "process_action_unavailable"
	processToolErrorNotFound                   processToolErrorCode = "process_not_found"
	processToolNextActionList                                       = toolcatalog.ToolNameListProcesses
)

func processToolError(
	code processToolErrorCode,
	message string,
	retryable bool,
	processID string,
	state executionstore.ProcessState,
	nextAction string,
) (toolResultContent, error) {
	result := struct {
		ErrorCode  processToolErrorCode        `json:"error_code"`
		Message    string                      `json:"message"`
		Error      string                      `json:"error"`
		Retryable  bool                        `json:"retryable"`
		ProcessID  string                      `json:"process_id,omitempty"`
		State      executionstore.ProcessState `json:"state,omitempty"`
		NextAction string                      `json:"next_action,omitempty"`
	}{
		ErrorCode:  code,
		Message:    message,
		Error:      message,
		Retryable:  retryable,
		ProcessID:  processID,
		State:      state,
		NextAction: nextAction,
	}
	return structuredToolResultContent(result)
}

func failProcessTransaction(
	cause error,
	code processToolErrorCode,
	message string,
	retryable bool,
	processID string,
	state executionstore.ProcessState,
	nextAction string,
) (transactionalPhaseResult, error) {
	content, err := processToolError(
		code,
		message,
		retryable,
		processID,
		state,
		nextAction,
	)
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, cause), nil
}

type processListObservation struct {
	ProcessID    string                      `json:"process_id"`
	State        executionstore.ProcessState `json:"state"`
	CommandLabel string                      `json:"command_label,omitempty"`
	Cwd          string                      `json:"cwd,omitempty"`
	StartedAt    string                      `json:"started_at,omitempty"`
	UpdatedAt    string                      `json:"updated_at,omitempty"`
}

func processListObservations(records []executionstore.ActiveProcessRecord) []processListObservation {
	out := make([]processListObservation, 0, len(records))
	for _, record := range records {
		processHandle, err := encodeProcessID(record.ID)
		if err != nil {
			continue
		}
		item := processListObservation{
			ProcessID:    processHandle,
			State:        record.State,
			CommandLabel: processcmd.CommandLabel(record.Command),
			Cwd:          record.Cwd,
			UpdatedAt:    record.UpdatedAt.UTC().Format(timeRFC3339Nano),
		}
		if record.SourceStartedAt != nil {
			item.StartedAt = record.SourceStartedAt.UTC().Format(timeRFC3339Nano)
		}
		out = append(out, item)
	}
	return out
}

func encodeProcessID(id storage.ID) (string, error) {
	return publicid.Encode(publicid.KindProcess, id)
}

func decodeProcessID(value string) (storage.ID, error) {
	return publicid.Decode(publicid.KindProcess, value)
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func writeProcessPayload(
	raw json.RawMessage,
) (string, json.RawMessage, error) {
	var input writeProcessRequest
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", nil, fmt.Errorf("parse write_process request: %w", err)
	}
	for key, value := range fields {
		if string(value) == "null" {
			return "", nil, fmt.Errorf(
				"write_process field %s cannot be null",
				key,
			)
		}
	}
	if err := decodeSingleStrictJSON(
		raw,
		&input,
		"write_process request",
	); err != nil {
		return "", nil, fmt.Errorf("parse write_process request: %w", err)
	}
	if input.ProcessID == "" {
		return "", nil, errors.New("write_process requires process_id")
	}
	closeStdin := input.CloseStdin != nil && *input.CloseStdin
	if (input.Data == nil || *input.Data == "") && !closeStdin {
		return "", nil, errors.New(
			"write_process requires non-empty data or close_stdin=true",
		)
	}
	if input.Data != nil && len(*input.Data) > processaction.MaxWriteBytes {
		return "", nil, fmt.Errorf(
			"write_process data cannot exceed %d bytes",
			processaction.MaxWriteBytes,
		)
	}
	payloadBody := processaction.WritePayload{}
	if input.Data != nil && *input.Data != "" {
		payloadBody.Data = *input.Data
	}
	if closeStdin {
		payloadBody.CloseStdin = true
	}
	payload, err := marshalJSON(payloadBody)
	if err != nil {
		return "", nil, fmt.Errorf("marshal write_process payload: %w", err)
	}
	return input.ProcessID, payload, nil
}

func resolveProcessObservationRequest(
	raw json.RawMessage,
	mode processObservationMode,
) (processObservationRequest, error) {
	if mode == processObservationList {
		if len(raw) != 0 {
			if strings.TrimSpace(string(raw)) == "null" {
				return processObservationRequest{}, errors.New(
					"list_processes request cannot be null",
				)
			}
			var empty struct{}
			if err := decodeSingleStrictJSON(raw, &empty, "list_processes request"); err != nil {
				return processObservationRequest{}, fmt.Errorf(
					"parse list_processes request: %w",
					err,
				)
			}
		}
		return processObservationRequest{Mode: mode}, nil
	}
	var parsed struct {
		ProcessID string `json:"process_id,omitempty"`
		Cursor    *int64 `json:"cursor,omitempty"`
		MaxBytes  *int   `json:"max_bytes,omitempty"`
		WaitMs    *int   `json:"wait_ms,omitempty"`
	}
	if len(raw) != 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return processObservationRequest{}, fmt.Errorf(
				"parse %s_process request: %w",
				mode,
				err,
			)
		}
		for key, value := range fields {
			if string(value) == "null" {
				return processObservationRequest{}, fmt.Errorf(
					"%s_process field %s cannot be null",
					mode,
					key,
				)
			}
		}
		if err := decodeSingleStrictJSON(raw, &parsed, string(mode)+"_process request"); err != nil {
			return processObservationRequest{}, fmt.Errorf(
				"parse %s_process request: %w",
				mode,
				err,
			)
		}
	}
	input := processObservationRequest{ProcessID: parsed.ProcessID, Mode: mode}
	if parsed.Cursor != nil {
		if *parsed.Cursor < 0 {
			return input, errors.New("cursor must be non-negative")
		}
		cursor := *parsed.Cursor
		input.Cursor = &cursor
	}
	if parsed.MaxBytes != nil {
		if *parsed.MaxBytes <= 0 {
			return input, errors.New("max_bytes must be positive")
		}
		if *parsed.MaxBytes > processaction.MaxObservationBytes {
			return input, fmt.Errorf(
				"max_bytes must be <= %d",
				processaction.MaxObservationBytes,
			)
		}
		input.MaxBytes = *parsed.MaxBytes
	}
	if parsed.WaitMs != nil {
		if *parsed.WaitMs <= 0 {
			return input, errors.New("wait_ms must be positive")
		}
		if *parsed.WaitMs > processaction.MaxWaitMilliseconds {
			return input, fmt.Errorf(
				"wait_ms must be <= %d",
				processaction.MaxWaitMilliseconds,
			)
		}
		input.WaitMS = *parsed.WaitMs
	}
	if mode != processObservationList {
		if input.ProcessID == "" {
			return input, fmt.Errorf("%s_process requires process_id", mode)
		}
	}
	return input, nil
}
