package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// toolAuthority is what lets a turn change a tool call: the agent runtime lock
// for model calls, or a worker claim for program calls. Losing it surfaces as
// storeerr.ErrRuntimeLockInactive either way.
type toolAuthority interface {
	ensureActive(context.Context, Executor, Turn) error
	recordedToolCall(context.Context, Executor, Turn, model.ToolCall) (executionstore.ToolCallRecord, error)
	markReady(context.Context, Executor, Turn, uuid.UUID) (executionstore.ToolCallRecord, error)
	openPermission(
		context.Context, Executor, Turn, uuid.UUID, toolpermission.Request,
	) (executionstore.AgentInteractionRecord, error)
	execute(
		context.Context, Executor, Turn, uuid.UUID, executionstore.ToolCallPlan,
	) (executionstore.ExecuteToolCallResult, error)
	// complete records a result for a call that is not running.
	complete(
		context.Context, Executor, Turn, uuid.UUID, executionstore.ToolResultOutcome, json.RawMessage,
	) (executionstore.ToolCallRecord, error)
	// completeRunning records the result of a call whose async phase ran.
	completeRunning(
		context.Context, Executor, Turn, uuid.UUID, executionstore.ToolResultOutcome, json.RawMessage,
	) (executionstore.ToolCallRecord, error)
	requeue(context.Context, Executor, Turn, uuid.UUID) error
}

// programCall reports whether the turn runs a program tool call, whose result
// goes only to the calling command.
func (t Turn) programCall() bool {
	_, ok := t.authority.(programClaimAuthority)
	return ok
}

func (t Turn) toolAuthority() toolAuthority {
	if t.authority == nil {
		return runtimeLockAuthority{}
	}
	return t.authority
}

type runtimeLockAuthority struct{}

func (runtimeLockAuthority) ensureActive(ctx context.Context, e Executor, turn Turn) error {
	return e.Store.Execution().EnsureRuntimeLockActive(ctx, turn.ProjectID, turn.AgentID, turn.RuntimeLockID)
}

func (runtimeLockAuthority) recordedToolCall(
	ctx context.Context, e Executor, turn Turn, call model.ToolCall,
) (executionstore.ToolCallRecord, error) {
	return e.recordedToolCall(ctx, turn, call)
}

func (runtimeLockAuthority) markReady(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID,
) (executionstore.ToolCallRecord, error) {
	return e.Store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ID: id, RuntimeLockID: turn.RuntimeLockID,
	})
}

func (runtimeLockAuthority) openPermission(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID, request toolpermission.Request,
) (executionstore.AgentInteractionRecord, error) {
	return e.Store.Execution().CreatePermissionInteraction(ctx, executionstore.CreatePermissionInteractionInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ToolCallID: id, RuntimeLockID: turn.RuntimeLockID,
		Request: request,
	})
}

func (runtimeLockAuthority) execute(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID, plan executionstore.ToolCallPlan,
) (executionstore.ExecuteToolCallResult, error) {
	return e.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ToolCallID: id, RuntimeLockID: turn.RuntimeLockID,
	}, plan)
}

func (runtimeLockAuthority) complete(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID,
	outcome executionstore.ToolResultOutcome, parts json.RawMessage,
) (executionstore.ToolCallRecord, error) {
	return e.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ID: id, Outcome: outcome,
		RuntimeLockID: turn.RuntimeLockID, ResultContentParts: parts,
	})
}

func (runtimeLockAuthority) completeRunning(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID,
	outcome executionstore.ToolResultOutcome, parts json.RawMessage,
) (executionstore.ToolCallRecord, error) {
	return e.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ID: id, RuntimeLockID: turn.RuntimeLockID,
		Outcome: outcome, ResultContentParts: parts,
	})
}

func (runtimeLockAuthority) requeue(ctx context.Context, e Executor, turn Turn, id uuid.UUID) error {
	return e.Store.Execution().RequeueRuntimeToolCall(ctx, executionstore.RequeueRuntimeToolCallInput{
		ProjectID: turn.ProjectID, AgentID: turn.AgentID, ToolCallID: id, RuntimeLockID: turn.RuntimeLockID,
	})
}

// MaxProgramToolResultBytes caps a program call's stored result content; the
// store does not move large program results to artifacts.
const MaxProgramToolResultBytes = 8 * 1024 * 1024

// programClaimAuthority acts on one program tool call under a worker claim.
type programClaimAuthority struct {
	ref   executionstore.ProgramToolCallRef
	lease time.Duration
}

func (a programClaimAuthority) refFor(id uuid.UUID) (executionstore.ProgramToolCallRef, error) {
	if id != a.ref.ToolCallID {
		return executionstore.ProgramToolCallRef{}, fmt.Errorf(
			"program tool claim does not cover tool call %s: %w", id, storeerr.ErrStateTransitionConflict)
	}
	return a.ref, nil
}

func (a programClaimAuthority) ensureActive(ctx context.Context, e Executor, _ Turn) error {
	_, err := e.Store.Execution().RenewProgramToolCallClaim(ctx, a.ref, a.lease)
	return programAuthorityError(err)
}

func (a programClaimAuthority) recordedToolCall(
	ctx context.Context, e Executor, turn Turn, call model.ToolCall,
) (executionstore.ToolCallRecord, error) {
	record, err := e.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, a.ref.ToolCallID)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	if record.ProviderCallID != call.ID || !toolCallMatches(record, call) {
		return executionstore.ToolCallRecord{}, storeerr.ErrIdempotencyConflict
	}
	return record, nil
}

func (a programClaimAuthority) markReady(
	ctx context.Context, e Executor, _ Turn, id uuid.UUID,
) (executionstore.ToolCallRecord, error) {
	ref, err := a.refFor(id)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	record, err := e.Store.Execution().MarkProgramToolCallReady(ctx, ref)
	return record.ToolCallRecord, programAuthorityError(err)
}

func (a programClaimAuthority) openPermission(
	ctx context.Context, e Executor, _ Turn, id uuid.UUID, request toolpermission.Request,
) (executionstore.AgentInteractionRecord, error) {
	ref, err := a.refFor(id)
	if err != nil {
		return executionstore.AgentInteractionRecord{}, err
	}
	interaction, err := e.Store.Execution().OpenProgramPermissionInteraction(ctx, ref, request)
	return interaction, programAuthorityError(err)
}

func (a programClaimAuthority) execute(
	ctx context.Context, e Executor, _ Turn, id uuid.UUID, plan executionstore.ToolCallPlan,
) (executionstore.ExecuteToolCallResult, error) {
	ref, err := a.refFor(id)
	if err != nil {
		return executionstore.ExecuteToolCallResult{}, err
	}
	result, err := e.Store.Execution().ExecuteProgramToolCall(ctx, ref, plan)
	return result, programAuthorityError(err)
}

func (a programClaimAuthority) complete(
	ctx context.Context, e Executor, _ Turn, id uuid.UUID,
	outcome executionstore.ToolResultOutcome, parts json.RawMessage,
) (executionstore.ToolCallRecord, error) {
	ref, err := a.refFor(id)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	outcome, parts, err = programToolResult(outcome, parts)
	if err != nil {
		return executionstore.ToolCallRecord{}, err
	}
	record, err := e.Store.Execution().CompleteProgramToolCall(ctx, executionstore.CompleteProgramToolCallInput{
		ProgramToolCallRef: ref, Outcome: outcome, ResultContentParts: parts,
	})
	return record.ToolCallRecord, programAuthorityError(err)
}

func (a programClaimAuthority) completeRunning(
	ctx context.Context, e Executor, turn Turn, id uuid.UUID,
	outcome executionstore.ToolResultOutcome, parts json.RawMessage,
) (executionstore.ToolCallRecord, error) {
	return a.complete(ctx, e, turn, id, outcome, parts)
}

// requeue fails the call: a claimed program call runs at most once.
func (a programClaimAuthority) requeue(ctx context.Context, e Executor, turn Turn, id uuid.UUID) error {
	content, err := structuredToolResultContent(map[string]any{
		"error_code": "async_tool_interrupted",
		"message":    "The tool call was interrupted before it ran. Retry the call.",
	})
	if err != nil {
		return err
	}
	parts, err := content.contentParts()
	if err != nil {
		return err
	}
	_, err = a.complete(ctx, e, turn, id, executionstore.ToolResultOutcomeFailed, parts)
	return err
}

func programAuthorityError(err error) error {
	if errors.Is(err, storeerr.ErrProgramToolCallClaimLost) || errors.Is(err, storeerr.ErrProcessTerminal) {
		return fmt.Errorf("%w: %w", storeerr.ErrRuntimeLockInactive, err)
	}
	return err
}

// programToolResult drops parts hidden from the model, which a program call
// has no use for, and fails results over MaxProgramToolResultBytes.
func programToolResult(
	outcome executionstore.ToolResultOutcome, parts json.RawMessage,
) (executionstore.ToolResultOutcome, json.RawMessage, error) {
	var decoded []json.RawMessage
	if err := json.Unmarshal(parts, &decoded); err != nil {
		return "", nil, fmt.Errorf("decode program tool result: %w", err)
	}
	visible := make([]json.RawMessage, 0, len(decoded))
	for _, part := range decoded {
		var flags struct {
			Exclude bool `json:"exclude_from_model_context"`
		}
		if err := json.Unmarshal(part, &flags); err != nil {
			return "", nil, fmt.Errorf("decode program tool result part: %w", err)
		}
		if !flags.Exclude {
			visible = append(visible, part)
		}
	}
	filtered, err := json.Marshal(visible)
	if err != nil {
		return "", nil, err
	}
	if len(filtered) <= MaxProgramToolResultBytes {
		return outcome, filtered, nil
	}
	content, err := structuredToolResultContent(map[string]any{
		"error_code": "result_too_large",
		"error": fmt.Sprintf("The tool result is %d bytes, over the %d byte limit for tool calls from commands.",
			len(filtered), MaxProgramToolResultBytes),
	})
	if err != nil {
		return "", nil, err
	}
	filtered, err = content.contentParts()
	return executionstore.ToolResultOutcomeFailed, filtered, err
}
