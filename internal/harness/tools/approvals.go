package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func (e Executor) PrepareToolCallPermission(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
) error {
	if _, ok, err := e.completedToolResult(ctx, turn, call); err != nil || ok {
		return err
	}
	proposal, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return err
	}
	if proposal.State != executionstore.ToolCallStateAwaitingAuthorization &&
		proposal.State != executionstore.ToolCallStateAwaitingPermission {
		return nil
	}
	spec, configured := turn.Tools[call.Name]
	if !configured {
		return e.completeInvalidToolCall(
			ctx,
			turn,
			proposal.ID,
			"unsupported",
			toolAvailabilityError(call.Name, turn.Tools, false),
		)
	}
	selection := spec.Permission
	toolType := spec.Type
	implementation, implemented, err := toolImplementationFor(call.Name)
	if err != nil {
		return err
	}
	modeHandler, modeDescriptor, supported, err := permissionModeForTool(
		toolType,
		selection,
		implementation,
		implemented,
	)
	if err != nil {
		return err
	}
	var inputErr error
	if implemented {
		inputErr = implementation.validateInput(call.Input)
	}
	if inputErr == nil && !implemented && supported {
		schema := spec.InputSchema
		if len(schema) == 0 {
			inputErr = fmt.Errorf("tool %q has no runtime input schema", call.Name)
		} else if err := jsonschema.Validate(schema, call.Input); err != nil {
			inputErr = fmt.Errorf("tool %q input: %w", call.Name, err)
		}
	}
	executable := implemented || toolType == toolcatalog.ToolTypeCustom
	unsupportedErr := toolAvailabilityError(call.Name, turn.Tools, executable)
	if inputErr != nil || unsupportedErr != nil {
		errorCode := "unsupported"
		if inputErr != nil {
			errorCode = "malformed"
		}
		cause := firstError(inputErr, unsupportedErr)
		return e.completeInvalidToolCall(ctx, turn, proposal.ID, errorCode, cause)
	}
	if !supported {
		return fmt.Errorf(
			"tool %q does not support permission mode %q",
			call.Name,
			selection.Mode,
		)
	}
	selection, err = toolpermission.ValidateSelection(
		selection,
		[]toolpermission.ModeDescriptor{modeDescriptor},
	)
	if err != nil {
		return fmt.Errorf("tool %q runtime permission: %w", call.Name, err)
	}
	if existing, found, err := e.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		proposal.ID,
		executionstore.AgentInteractionKindPermission,
	); err != nil {
		return err
	} else if found {
		return e.evaluateExistingPermissionInteraction(
			ctx,
			turn,
			call,
			proposal.ID,
			selection,
			existing,
		)
	}
	if proposal.State == executionstore.ToolCallStateAwaitingPermission {
		return storeerr.ErrIdempotencyConflict
	}
	modeResult, err := modeHandler(
		ctx,
		e,
		turn,
		call,
		permissionModeContext{
			selection:  selection,
			descriptor: modeDescriptor,
		},
	)
	var preparationErr *toolCallPreparationError
	if errors.As(err, &preparationErr) {
		return e.completeToolCallPreparationFailure(
			ctx,
			turn,
			proposal.ID,
			preparationErr,
		)
	}
	if err != nil {
		return err
	}
	switch modeResult.kind {
	case permissionModeAllow:
		if _, err := e.Store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID:     turn.ProjectID,
			AgentID:       turn.AgentID,
			ID:            proposal.ID,
			RuntimeLockID: turn.RuntimeLockID,
		}); err != nil {
			return err
		}
		return nil
	case permissionModeDeny:
		return e.completeDeniedToolCall(
			ctx,
			turn,
			proposal.ID,
			modeResult.reason,
		)
	case permissionModeAsk:
	default:
		return fmt.Errorf("tool %q permission mode returned an invalid outcome", call.Name)
	}
	interaction, err := e.Store.Execution().CreatePermissionInteraction(
		ctx,
		executionstore.CreatePermissionInteractionInput{
			ProjectID:     turn.ProjectID,
			AgentID:       turn.AgentID,
			ToolCallID:    proposal.ID,
			RuntimeLockID: turn.RuntimeLockID,
			Request:       modeResult.request,
		},
	)
	if err != nil {
		return err
	}
	e.enqueueIntegrationPromptCopy(turn, interaction)
	return nil
}

func (e Executor) evaluateExistingPermissionInteraction(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
	toolCallID storage.ID,
	selection toolpermission.Selection,
	permission executionstore.AgentInteractionRecord,
) error {
	if !toolCallPermissionMatches(permission, call, toolCallID, selection) {
		return storeerr.ErrIdempotencyConflict
	}
	switch permission.State {
	case executionstore.AgentInteractionStateOpen:
		if err := e.Store.Execution().EnsureRuntimeLockActive(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			turn.RuntimeLockID,
		); err != nil {
			return err
		}
		return nil
	case executionstore.AgentInteractionStateResolved:
		toolCall, err := e.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, toolCallID)
		if err != nil {
			return err
		}
		if toolCall.State == executionstore.ToolCallStateReady ||
			toolCall.State == executionstore.ToolCallStateRunning ||
			toolCall.State == executionstore.ToolCallStateWaiting ||
			(toolCall.State == executionstore.ToolCallStateCompleted &&
				toolCall.Outcome != executionstore.ToolResultOutcomeDenied &&
				toolCall.Outcome != executionstore.ToolResultOutcomeCanceled) {
			return nil
		}
		return e.assertTerminalPermissionInteraction(ctx, turn, toolCallID)
	case executionstore.AgentInteractionStateCanceled:
		return e.assertTerminalPermissionInteraction(ctx, turn, toolCallID)
	default:
		return fmt.Errorf(
			"permission interaction %s has unsupported state %q",
			permission.ID,
			permission.State,
		)
	}
}

func (e Executor) assertTerminalPermissionInteraction(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
) error {
	toolCall, err := e.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, toolCallID)
	if err != nil {
		return err
	}
	if toolCall.State == executionstore.ToolCallStateCompleted {
		return nil
	}
	return fmt.Errorf(
		"terminal permission interaction for tool call %s did not complete its execution: %w",
		toolCallID,
		storeerr.ErrIdempotencyConflict,
	)
}

func (e Executor) completeInvalidToolCall(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	errorCode string,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	content, err := structuredToolResultContent(
		map[string]any{"error": cause.Error(), "error_code": errorCode},
	)
	if err != nil {
		return fmt.Errorf("marshal invalid tool result: %w", err)
	}
	contentParts, err := content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal invalid tool content parts: %w", err)
	}
	_, err = e.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          turn.ProjectID,
			AgentID:            turn.AgentID,
			ID:                 toolCallID,
			Outcome:            executionstore.ToolResultOutcomeFailed,
			RuntimeLockID:      turn.RuntimeLockID,
			ResultContentParts: contentParts,
		},
	)
	return err
}

func (e Executor) completeToolCallPreparationFailure(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	failure *toolCallPreparationError,
) error {
	contentParts, err := failure.content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal tool preparation failure content parts: %w", err)
	}
	_, err = e.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          turn.ProjectID,
			AgentID:            turn.AgentID,
			ID:                 toolCallID,
			Outcome:            executionstore.ToolResultOutcomeFailed,
			RuntimeLockID:      turn.RuntimeLockID,
			ResultContentParts: contentParts,
		},
	)
	return err
}

func firstError(left error, right error) error {
	if left != nil {
		return left
	}
	return right
}

func (e Executor) completeDeniedToolCall(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	reason string,
) error {
	if reason == "" {
		reason = "tool call was denied"
	}
	content, err := structuredToolResultContent(map[string]any{"reason": reason})
	if err != nil {
		return fmt.Errorf("marshal denied tool result: %w", err)
	}
	contentParts, err := content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal denied tool content parts: %w", err)
	}
	_, err = e.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          turn.ProjectID,
			AgentID:            turn.AgentID,
			ID:                 toolCallID,
			Outcome:            executionstore.ToolResultOutcomeDenied,
			RuntimeLockID:      turn.RuntimeLockID,
			ResultContentParts: contentParts,
		},
	)
	return err
}

func (e Executor) completeAsyncToolFailure(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	content toolResultContent,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	errorMessage := cause.Error()
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		errorMessage = executionstore.RuntimeToolInterruptedMessage
		var err error
		content, err = structuredToolResultContent(map[string]any{
			"code":    "async_tool_interrupted",
			"message": errorMessage,
		})
		if err != nil {
			return fmt.Errorf("marshal interrupted async tool result: %w", err)
		}
	}
	if !content.isSet {
		var err error
		content, err = structuredToolResultContent(
			map[string]any{"error": errorMessage},
		)
		if err != nil {
			return fmt.Errorf("marshal async tool failure result: %w", err)
		}
	}
	contentParts, err := content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal async tool failure content parts: %w", err)
	}
	err = retryAsyncToolPersistence(ctx, func(ctx context.Context) error {
		_, err := e.Store.Execution().CompleteRuntimeToolCall(
			ctx,
			executionstore.CompleteRuntimeToolCallInput{
				ProjectID:          turn.ProjectID,
				AgentID:            turn.AgentID,
				ID:                 toolCallID,
				RuntimeLockID:      turn.RuntimeLockID,
				Outcome:            executionstore.ToolResultOutcomeFailed,
				ResultContentParts: contentParts,
			},
		)
		return err
	})
	if errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		return nil
	}
	return err
}

func (e Executor) completeAsyncToolSuccess(
	ctx context.Context,
	turn Turn,
	toolCallID storage.ID,
	content toolResultContent,
) error {
	contentParts, err := content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal async tool success content parts: %w", err)
	}
	err = retryAsyncToolPersistence(ctx, func(ctx context.Context) error {
		_, err := e.Store.Execution().CompleteRuntimeToolCall(
			ctx,
			executionstore.CompleteRuntimeToolCallInput{
				ProjectID:          turn.ProjectID,
				AgentID:            turn.AgentID,
				ID:                 toolCallID,
				RuntimeLockID:      turn.RuntimeLockID,
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: contentParts,
			},
		)
		return err
	})
	if err == nil {
		return nil
	}
	logpkg.LoggerFromContext(ctx).Error(
		"persist async tool success",
		"tool_call_id", toolCallID,
		"error", err,
	)
	const message = "The tool ran successfully, but its result could not be stored."
	fallback, fallbackErr := structuredToolResultContent(map[string]any{
		"code":    "tool_result_persistence_failed",
		"message": message,
	})
	if fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	if fallbackErr := e.completeAsyncToolFailure(
		ctx,
		turn,
		toolCallID,
		fallback,
		errors.New("tool result persistence failed"),
	); fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	return nil
}
