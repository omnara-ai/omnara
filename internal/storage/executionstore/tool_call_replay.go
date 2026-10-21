package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type toolCallReplay func(context.Context, *toolCallTransaction, ToolCallRecord) (any, error)

type replayableCommand struct {
	run    toolCallCommandFunc
	replay toolCallReplay
}

func replayableToolCommand(replay toolCallReplay, run toolCallCommandFunc) ToolCallCommand {
	return replayableCommand{run: run, replay: replay}
}

func (c replayableCommand) apply(ctx context.Context, t *toolCallTransaction) (any, error) {
	return c.run(ctx, t)
}

func (c replayableCommand) resolve(ctx context.Context, t *toolCallTransaction) (any, bool, error) {
	call, err := getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	switch call.State {
	case ToolCallStateCompleted:
		t.disposition = ToolCallDispositionCompleted
		t.completed = &call
	case ToolCallStateWaiting:
		t.disposition = ToolCallDispositionWaiting
	default:
		return nil, false, nil
	}
	result, err := c.replay(ctx, t, call)
	if err != nil {
		return nil, true, err
	}
	if _, err := lockExecution(ctx, t.unit, t.input.ProjectID, t.input.AgentID, false, uuid.Nil); err != nil {
		return nil, true, err
	}
	if call.State == ToolCallStateWaiting {
		current, err := getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
		if err != nil {
			return nil, true, err
		}
		if current.State == ToolCallStateCompleted {
			t.completed = &current
			t.disposition = ToolCallDispositionCompleted
		}
	}
	return result, true, nil
}

func replayCompletion(input ToolCallCompletionInput) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		if _, err := parseToolResultContentBlocks(input.ResultContentParts); err != nil {
			return nil, err
		}
		if call.State != ToolCallStateCompleted || call.Outcome != input.Outcome {
			return nil, storeerr.ErrIdempotencyConflict
		}
		return call, nil
	}
}

func replayAsyncTool(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
	if call.State != ToolCallStateCompleted {
		return nil, storeerr.ErrIdempotencyConflict
	}
	return call, nil
}

func replayProcessCommand(input CreateProcessInput) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		record, found, err := t.replayProcess(ctx, input)
		if err == nil && !found {
			err = storeerr.ErrIdempotencyConflict
		}
		return record, err
	}
}

func replayProcessActionCommand(input CreateProcessActionInput, stopped *ToolCallCompletionInput) toolCallReplay {
	input.Payload = normalizedJSON(input.Payload)
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		record, found, err := t.replayProcessAction(ctx, input)
		if err != nil || found {
			return record, err
		}
		if stopped != nil {
			return replayCompletion(*stopped)(ctx, t, call)
		}
		return nil, storeerr.ErrIdempotencyConflict
	}
}

func replayQuestionCommand(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
	return t.replayQuestion(ctx)
}

func (t *toolCallTransaction) replayQuestion(ctx context.Context) (AgentInteractionRecord, error) {
	row, err := t.q.GetAgentInteractionByToolCallKind(ctx, dbsqlc.GetAgentInteractionByToolCallKindParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
		InteractionKind: string(AgentInteractionKindQuestion),
	})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	t.hasDurableCompletionOwner = true
	t.owner.InteractionID = row.ID
	return agentInteractionRecordFromSQLC(row), nil
}

func replayCreateMachine(
	input CreatePoolMachineInput,
	completion ToolCallCompletionBuilder[CreatePoolMachineResult],
) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		record, found, err := poolMachineByCreateToolCallTx(ctx, t.q, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
		if err != nil {
			return nil, err
		}
		if !found || record.Machine.MachinePoolID != input.MachinePoolID {
			return nil, storeerr.ErrIdempotencyConflict
		}
		return replayBuiltCompletion(ctx, t, call, CreatePoolMachineResult{Machine: record}, completion)
	}
}

func replayDeleteMachine(
	input DeletePoolMachineInput,
	completion ToolCallCompletionBuilder[PoolMachineRecord],
) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		record, err := poolMachineByIDTx(ctx, t.q, t.input.ProjectID, t.input.AgentID, input.MachineID)
		if err != nil {
			return nil, err
		}
		if record.Binding.DeleteToolCallID != t.input.ToolCallID {
			return nil, storeerr.ErrIdempotencyConflict
		}
		return replayBuiltCompletion(ctx, t, call, record, completion)
	}
}

func replayLaunchSubagent(
	input LaunchAgentInput,
	completion ToolCallCompletionBuilder[LaunchAgentResult],
) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		input, err := validateLaunchAgentInput(input)
		if err != nil {
			return nil, err
		}
		if input.Subagent == nil || input.Subagent.ParentAgentID != t.input.AgentID {
			return nil, storeerr.ErrIdempotencyConflict
		}
		result, found, err := launchReplayMaybeTx(ctx, t.q, input)
		if err != nil {
			return nil, err
		}
		if !found || result.Agent.ParentAgentID != t.input.AgentID {
			return nil, storeerr.ErrIdempotencyConflict
		}
		return replayBuiltCompletion(ctx, t, call, result, completion)
	}
}

func replayInteractionSelection(
	input SelectInteractionHandlerInput,
	completion ToolCallCompletionBuilder[InteractionSelection],
) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		return replayBuiltCompletion(ctx, t, call, InteractionSelection{HandlerKey: input.HandlerKey}, completion)
	}
}

func replayBuiltCompletion[T any](ctx context.Context, t *toolCallTransaction, call ToolCallRecord,
	result T, completion ToolCallCompletionBuilder[T],
) (any, error) {
	if completion == nil {
		return nil, errors.New("tool completion builder is required")
	}
	input, err := completion(result)
	if err != nil {
		return nil, err
	}
	if _, err := replayCompletion(input)(ctx, t, call); err != nil {
		return nil, err
	}
	return result, nil
}

func replaySubagentCompletion(target uuid.UUID, completion ToolCallCompletionInput) toolCallReplay {
	return func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		child, err := loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, target)
		if err != nil {
			return nil, err
		}
		if child.ParentAgentID != t.input.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("target agent is not a subagent of this agent"))
		}
		return replayCompletion(completion)(ctx, t, call)
	}
}

func recordedToolReplay() ToolCallCommand {
	return replayableToolCommand(func(ctx context.Context, t *toolCallTransaction, call ToolCallRecord) (any, error) {
		if call.State == ToolCallStateCompleted {
			return call, nil
		}
		question, err := replayQuestionCommand(ctx, t, call)
		if err == nil {
			return question, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		process, err := t.q.GetProcessByToolCall(ctx, dbsqlc.GetProcessByToolCallParams{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
		})
		if err == nil {
			t.hasDurableCompletionOwner = true
			t.owner.ProcessID = process.ID
			return processRecordFromSQLC(process), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		action, err := t.q.GetProcessActionByToolCall(ctx, dbsqlc.GetProcessActionByToolCallParams{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
		})
		if err != nil {
			return nil, err
		}
		t.hasDurableCompletionOwner = true
		t.owner.ActionID = action.ID
		return processActionRecordFromSQLC(action), nil
	}, func(context.Context, *toolCallTransaction) (any, error) { return nil, storeerr.ErrIdempotencyConflict })
}
