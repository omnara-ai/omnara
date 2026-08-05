package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (e Executor) Dispatch(ctx context.Context, turn Turn, call model.ToolCall) (Result, error) {
	if call.ID == "" || call.Name == "" {
		return Result{}, errors.New("tool call id and name are required")
	}
	proposal, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return Result{}, fmt.Errorf("load tool call %s: %w", call.ID, err)
	}
	if proposal.State == executionstore.ToolCallStateCompleted {
		return Result{
			ToolCallID:   call.ID,
			Name:         call.Name,
			ContentParts: proposal.ResultContentParts,
			Disposition:  DispatchCompleted,
		}, nil
	}
	implementation, implemented, err := toolImplementationFor(call.Name)
	if err != nil {
		return Result{}, err
	}
	var inputErr error
	if implemented {
		inputErr = implementation.validateInput(call.Input)
	}
	unsupportedErr := toolAvailabilityError(call.Name, turn.Tools, implemented)
	var content toolResultContent
	var dispatchResult toolDispatchResult
	execErr := inputErr
	outcome := executionstore.ToolResultOutcomeSucceeded
	errorCode := ""
	if inputErr != nil {
		errorCode = "malformed"
	}
	if execErr == nil && unsupportedErr != nil {
		execErr = unsupportedErr
		errorCode = "unsupported"
	}
	if execErr == nil &&
		(proposal.State == executionstore.ToolCallStateAwaitingAuthorization ||
			proposal.State == executionstore.ToolCallStateAwaitingPermission) {
		return Result{}, ErrActionRequired
	}
	if execErr == nil {
		dispatchResult, err = e.dispatchToolHandler(
			ctx,
			turn,
			call,
			proposal.ID,
			implementation.handler,
		)
		if err != nil {
			err = fmt.Errorf("dispatch tool %s: %w", call.Name, err)
			if errors.Is(err, ErrToolAuthorizationInvalidated) {
				execErr = err
			} else {
				return Result{}, err
			}
		}
	}
	if execErr == nil {
		switch dispatchResult := dispatchResult.(type) {
		case toolDispatchAwaiting:
			return Result{
				ToolCallID:  call.ID,
				Name:        call.Name,
				Disposition: DispatchDeferred,
			}, nil
		case toolDispatchCompleted:
			if replayed, ok, err := e.completedToolResult(ctx, turn, call); err != nil {
				return Result{}, err
			} else if ok {
				return replayed, nil
			}
			return Result{}, fmt.Errorf(
				"tool %s completed during dispatch but no result is available",
				call.Name,
			)
		case toolDispatchFailed:
			content = dispatchResult.content
			execErr = dispatchResult.cause
		case nil:
			execErr = errors.New("tool dispatch returned no result")
		default:
			execErr = fmt.Errorf(
				"unsupported tool dispatch result %T",
				dispatchResult,
			)
		}
	}
	if errors.Is(execErr, ErrActionRequired) {
		return Result{}, execErr
	}
	if errors.Is(execErr, storeerr.ErrIdempotencyConflict) {
		return Result{}, execErr
	}
	if errors.Is(execErr, storeerr.ErrRuntimeLockInactive) {
		return Result{}, execErr
	}
	if execErr != nil {
		outcome = executionstore.ToolResultOutcomeFailed
		if inputErr != nil {
			errorCode = "malformed"
		}
		if errors.Is(execErr, ErrToolAuthorizationInvalidated) {
			content, err = structuredToolResultContent(map[string]any{
				"error_code": ErrToolAuthorizationInvalidated.Error(),
				"error":      "The approved tool request changed before it could run. Submit a new tool call for approval.",
			})
			if err != nil {
				return Result{}, fmt.Errorf("marshal invalidated authorization result: %w", err)
			}
		}
		if !content.isSet {
			result := map[string]any{"error": execErr.Error()}
			if errorCode != "" {
				result["error_code"] = errorCode
			}
			content, err = structuredToolResultContent(result)
			if err != nil {
				return Result{}, fmt.Errorf("marshal failed tool result: %w", err)
			}
		}
	}
	contentParts, err := content.contentParts()
	if err != nil {
		return Result{}, fmt.Errorf("marshal tool result content parts: %w", err)
	}
	completed, err := e.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          turn.ProjectID,
			AgentID:            turn.AgentID,
			ID:                 proposal.ID,
			Outcome:            outcome,
			RuntimeLockID:      turn.RuntimeLockID,
			ResultContentParts: contentParts,
		},
	)
	if err != nil {
		return Result{}, fmt.Errorf("complete tool call: %w", err)
	}
	return Result{
		ToolCallID:   call.ID,
		Name:         call.Name,
		ContentParts: completed.ResultContentParts,
		Disposition:  DispatchCompleted,
	}, nil
}

func (e Executor) FailRunnableToolCall(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
	cause error,
) error {
	if cause == nil {
		return errors.New("tool call failure cause is required")
	}
	record, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return err
	}
	runnable := record.State == executionstore.ToolCallStateAwaitingAuthorization ||
		(record.State == executionstore.ToolCallStateReady &&
			toolcatalog.IsPlatformManagedToolType(record.Type))
	if !runnable {
		return storeerr.ErrStateTransitionConflict
	}
	content, err := structuredToolResultContent(map[string]any{
		"error_code": "tool_work_no_progress",
		"error":      cause.Error(),
	})
	if err != nil {
		return err
	}
	contentParts, err := content.contentParts()
	if err != nil {
		return err
	}
	_, err = e.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          turn.ProjectID,
			AgentID:            turn.AgentID,
			ID:                 record.ID,
			Outcome:            executionstore.ToolResultOutcomeFailed,
			RuntimeLockID:      turn.RuntimeLockID,
			ResultContentParts: contentParts,
		},
	)
	return err
}

type machineUnavailableResult struct {
	Cause   error
	Content toolResultContent
}

func machineUnavailableToolResult(cause error) (machineUnavailableResult, error) {
	errorCode := ErrNoActiveAgentMachineBinding.Error()
	if cause != nil {
		errorCode = cause.Error()
	}
	message := errorCode
	body := map[string]any{"error": message, "error_code": errorCode}
	if errors.Is(cause, ErrMachineSelectionRequired) {
		body["error"] = "machine_ref is required when multiple machines are available"
		body["error_code"] = ErrMachineSelectionRequired.Error()
		content, marshalErr := structuredToolResultContent(body)
		if marshalErr != nil {
			return machineUnavailableResult{}, marshalErr
		}
		return machineUnavailableResult{Cause: ErrMachineSelectionRequired, Content: content}, nil
	}
	if errors.Is(cause, ErrMachineRefUnavailable) {
		body["error"] = "machine_ref is unavailable"
		body["error_code"] = ErrMachineRefUnavailable.Error()
		content, marshalErr := structuredToolResultContent(body)
		if marshalErr != nil {
			return machineUnavailableResult{}, marshalErr
		}
		return machineUnavailableResult{Cause: ErrMachineRefUnavailable, Content: content}, nil
	}
	content, marshalErr := structuredToolResultContent(body)
	if marshalErr != nil {
		return machineUnavailableResult{}, marshalErr
	}
	return machineUnavailableResult{Cause: errors.New(errorCode), Content: content}, nil
}

func (e Executor) completedToolResult(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
) (Result, bool, error) {
	record, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return Result{}, false, err
	}
	if !toolCallMatches(record, call) {
		return Result{}, false, storeerr.ErrIdempotencyConflict
	}
	if record.State != executionstore.ToolCallStateCompleted {
		return Result{}, false, nil
	}
	parts := record.ResultContentParts
	return Result{
		ToolCallID:   call.ID,
		Name:         call.Name,
		ContentParts: parts,
		Disposition:  DispatchCompleted,
	}, true, nil
}

func (e Executor) recordedToolCall(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
) (executionstore.ToolCallRecord, error) {
	record, found, err := e.Store.Execution().GetToolCallByProviderCall(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.ModelCallContextID,
		call.ID,
	)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	if !found {
		return executionstore.ToolCallRecord{}, fmt.Errorf("tool call %s has not been recorded", call.ID)
	}
	if !toolCallMatches(record, call) {
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
	}
	return record, nil
}

func toolCallMatches(record executionstore.ToolCallRecord, call model.ToolCall) bool {
	return record.Name == call.Name && jsoncanonical.Equal(record.Input, call.Input)
}

func toolAvailabilityError(name string, available map[string]ToolSpec, implemented bool) error {
	if available != nil {
		if _, enabled := available[name]; !enabled {
			return fmt.Errorf("tool %q is not enabled for this agent config", name)
		}
	}
	if implemented {
		return nil
	}
	return fmt.Errorf("unsupported tool %q", name)
}
