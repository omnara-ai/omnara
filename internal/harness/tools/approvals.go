package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

func (e Executor) PrepareRecordedToolCallPermission(
	ctx context.Context, turn Turn, proposal executionstore.ToolCallRecord,
) (executionstore.ToolCallRecord, error) {
	if proposal.ProjectID != turn.ProjectID || proposal.AgentID != turn.AgentID ||
		proposal.ModelCallContextID != turn.ModelCallContextID {
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
	}
	call := model.ToolCall{ID: proposal.ProviderCallID, Name: proposal.Name, Input: proposal.Input}
	if proposal.State != executionstore.ToolCallStateAwaitingAuthorization &&
		proposal.State != executionstore.ToolCallStateAwaitingPermission {
		return proposal, nil
	}
	spec, configured := turn.Tools[call.Name]
	if !configured {
		return executionstore.ToolCallRecord{}, e.completeInvalidToolCall(
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
		return executionstore.ToolCallRecord{}, err
	}
	modeHandler, modeDescriptor, supported, err := permissionModeForTool(
		toolType,
		selection,
		implementation,
		implemented,
	)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	var inputErr error
	if implemented {
		inputErr = implementation.validateInput(call.Input)
	}
	if inputErr == nil && supported && (!implemented || toolcatalog.UsesIntegrationToolNamespace(call.Name)) {
		schema := spec.InputSchema
		if len(schema) == 0 {
			inputErr = fmt.Errorf("tool \"%s\" has no runtime input schema", call.Name)
		} else if err := jsonschema.Validate(schema, call.Input); err != nil {
			inputErr = fmt.Errorf("tool \"%s\" input: %w", call.Name, err)
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
		return executionstore.ToolCallRecord{}, e.completeInvalidToolCall(ctx, turn, proposal.ID, errorCode, cause)
	}
	if !supported {
		return executionstore.ToolCallRecord{}, fmt.Errorf(
			"tool \"%s\" does not support permission mode \"%s\"",
			call.Name,
			selection.Mode,
		)
	}
	selection, err = toolpermission.ValidateSelection(
		selection,
		[]toolpermission.ModeDescriptor{modeDescriptor},
	)
	if err != nil {
		return executionstore.ToolCallRecord{}, fmt.Errorf("tool \"%s\" runtime permission: %w", call.Name, err)
	}
	if existing, found, err := e.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		proposal.ID,
		executionstore.AgentInteractionKindPermission,
	); err != nil {
		return executionstore.ToolCallRecord{}, err
	} else if found {
		return executionstore.ToolCallRecord{}, e.evaluateExistingPermissionInteraction(
			ctx,
			turn,
			call,
			proposal.ID,
			selection,
			existing,
		)
	}
	if proposal.State == executionstore.ToolCallStateAwaitingPermission {
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
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
		return executionstore.ToolCallRecord{}, e.completeToolCallPreparationFailure(
			ctx,
			turn,
			proposal.ID,
			preparationErr,
		)
	}
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	switch modeResult.kind {
	case permissionModeAllow:
		return turn.toolAuthority().markReady(ctx, e, turn, proposal.ID)
	case permissionModeDeny:
		return executionstore.ToolCallRecord{}, e.completeDeniedToolCall(
			ctx,
			turn,
			proposal.ID,
			modeResult.reason,
		)
	case permissionModeAsk:
	default:
		return executionstore.ToolCallRecord{}, fmt.Errorf(
			"tool \"%s\" permission mode returned an invalid outcome",
			call.Name,
		)
	}
	interaction, err := turn.toolAuthority().openPermission(ctx, e, turn, proposal.ID, modeResult.request)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	e.enqueueIntegrationPromptCopy(turn, interaction)
	return executionstore.ToolCallRecord{}, nil
}

// programApprovalHolds checks, before a program call a user approved runs,
// that the agent's current config still permits it: the tool is always
// allowed, or its permission is the one the approval was given under. It
// completes the call denied and reports false otherwise, since the user was
// not asked under the permission now in force.
func (e Executor) programApprovalHolds(
	ctx context.Context, turn Turn, record executionstore.ToolCallRecord,
) (bool, error) {
	spec, configured := turn.Tools[record.Name]
	if !configured {
		// Dispatch fails a call to a tool that is no longer available.
		return true, nil
	}
	selection, err := toolpermission.NormalizeSelection(spec.Permission)
	if err != nil {
		return false, fmt.Errorf("tool \"%s\" runtime permission: %w", record.Name, err)
	}
	if selection.Mode == toolpermission.ModeAlwaysAllow {
		return true, nil
	}
	if selection.Mode != toolpermission.ModeAlwaysDeny {
		permission, found, err := e.Store.Execution().GetAgentInteractionByToolCallKind(
			ctx, turn.ProjectID, turn.AgentID, record.ID, executionstore.AgentInteractionKindPermission)
		if err != nil {
			return false, err
		}
		call := model.ToolCall{ID: record.ProviderCallID, Name: record.Name, Input: record.Input}
		if found && toolCallPermissionMatches(permission, call, record.ID, selection) {
			return true, nil
		}
	}
	return false, e.completeDeniedToolCall(ctx, turn, record.ID,
		"The tool's permission changed after this call was approved. Make the call again.")
}

func (e Executor) evaluateExistingPermissionInteraction(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
	toolCallID uuid.UUID,
	selection toolpermission.Selection,
	permission executionstore.AgentInteractionRecord,
) error {
	if !toolCallPermissionMatches(permission, call, toolCallID, selection) {
		return storeerr.ErrIdempotencyConflict
	}
	switch permission.State {
	case executionstore.AgentInteractionStateOpen:
		if err := turn.toolAuthority().ensureActive(ctx, e, turn); err != nil {
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
			"permission interaction %s has unsupported state \"%s\"",
			permission.ID,
			permission.State,
		)
	}
}

func (e Executor) assertTerminalPermissionInteraction(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
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
	toolCallID uuid.UUID,
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
	_, err = turn.toolAuthority().complete(
		ctx, e, turn, toolCallID, executionstore.ToolResultOutcomeFailed, contentParts,
	)
	return err
}

func (e Executor) completeToolCallPreparationFailure(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
	failure *toolCallPreparationError,
) error {
	contentParts, err := failure.content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal tool preparation failure content parts: %w", err)
	}
	_, err = turn.toolAuthority().complete(
		ctx, e, turn, toolCallID, executionstore.ToolResultOutcomeFailed, contentParts,
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
	toolCallID uuid.UUID,
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
	_, err = turn.toolAuthority().complete(
		ctx, e, turn, toolCallID, executionstore.ToolResultOutcomeDenied, contentParts,
	)
	return err
}

func (e Executor) completeAsyncToolFailure(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
	content toolResultContent,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	errorMessage := cause.Error()
	// Provider failures may carry a confirmed receipt or delivery uncertainty.
	// Preserve that evidence even when the underlying cause is a timeout.
	if !content.isSet && (errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
		errorMessage = executionpolicy.InterruptedToolMessage
		var err error
		content, err = structuredToolResultContent(map[string]any{
			"error_code": "async_tool_interrupted",
			"message":    errorMessage,
		})
		if err != nil {
			return fmt.Errorf("marshal interrupted async tool result: %w", err)
		}
	}
	if !content.isSet {
		failure := map[string]any{"error": errorMessage}
		if code := storeerr.FileErrorCode(cause); code != "" {
			failure["error_code"] = code
		} else if errors.Is(cause, errFileToolUnavailable) {
			failure["error_code"] = "operation_unavailable"
		}
		var err error
		content, err = structuredToolResultContent(failure)
		if err != nil {
			return fmt.Errorf("marshal async tool failure result: %w", err)
		}
	}
	err := e.completeAsyncToolResult(ctx, turn, toolCallID, content, executionstore.ToolResultOutcomeFailed)
	if errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		return nil
	}
	return err
}

func (e Executor) completeAsyncToolResult(
	ctx context.Context,
	turn Turn,
	toolCallID uuid.UUID,
	content toolResultContent,
	outcome executionstore.ToolResultOutcome,
) error {
	parts, err := content.contentParts()
	if err != nil {
		return fmt.Errorf("marshal async tool result: %w", err)
	}
	persist := func(ctx context.Context) error {
		_, err := turn.toolAuthority().completeRunning(ctx, e, turn, toolCallID, outcome, parts)
		return err
	}
	err = retryAsyncToolPersistence(ctx, persist)
	if err == nil || errors.Is(err, storeerr.ErrRuntimeLockInactive) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict) || errors.Is(err, storeerr.ErrIdempotencyConflict) ||
		errors.Is(err, storeerr.ErrInvalidToolCallDisposition) || errors.Is(err, storeerr.ErrNotFound) {
		return err
	}
	code := "tool_result_persistence_failed"
	message := "The tool ran successfully, but its result could not be stored."
	if outcome == executionstore.ToolResultOutcomeFailed {
		message = "The tool failed, but its error result could not be stored."
	}
	if errors.Is(err, storeerr.ErrInvalidRequest) {
		code = "tool_result_invalid"
		message = "The tool returned an invalid result."
	} else {
		logpkg.LoggerFromContext(ctx).Error("persist async tool result", "tool_call_id", toolCallID, "error", err)
	}
	fallback, fallbackErr := structuredToolResultContent(map[string]any{
		"error_code": code,
		"message":    message,
	})
	if fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	parts, fallbackErr = fallback.contentParts()
	if fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	outcome = executionstore.ToolResultOutcomeFailed
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncToolCompletionTimeout)
	defer cancel()
	if fallbackErr := retryAsyncToolPersistence(completionCtx, persist); fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	return nil
}
