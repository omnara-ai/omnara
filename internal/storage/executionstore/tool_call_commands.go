package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type toolCallCommandFunc func(context.Context, *toolCallTransaction) (any, error)

type ToolCallCompletionBuilder[T any] func(T) (ToolCallCompletionInput, error)

func CompleteToolCallInExecution(input ToolCallCompletionInput) ToolCallCommand {
	replay := replayCompletion(input)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		return tx.completeToolCall(ctx, input)
	})
}

func StartToolCallAsync() ToolCallCommand {
	replay := replayAsyncTool
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		return nil, tx.startToolCall(ctx, true)
	})
}

func StartProcessForToolCall(input CreateProcessInput) ToolCallCommand {
	replay := replayProcessCommand(input)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.startProcess(ctx, input)
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, false)
	})
}

func CreateProcessActionForToolCall(input CreateProcessActionInput) ToolCallCommand {
	replay := replayProcessActionCommand(input, nil)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.createProcessAction(ctx, input)
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, false)
	})
}

func StopProcessForToolCall(
	input CreateProcessActionInput,
	alreadyStoppedCompletion ToolCallCompletionInput,
) ToolCallCommand {
	replay := replayProcessActionCommand(input, &alreadyStoppedCompletion)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.createProcessAction(ctx, input)
		if errors.Is(err, storeerr.ErrProcessAlreadyStopped) {
			if tx.disposition == ToolCallDispositionCompleted {
				return tx.completeToolCall(ctx, alreadyStoppedCompletion)
			}
			if err := tx.q.TouchProcessActivity(ctx, dbsqlc.TouchProcessActivityParams{
				ProjectID: tx.input.ProjectID,
				AgentID:   tx.input.AgentID,
				ProcessID: input.ProcessID,
			}); err != nil {
				return nil, fmt.Errorf("touch already-stopped process activity: %w", err)
			}
			return tx.completeToolCall(ctx, alreadyStoppedCompletion)
		}
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, false)
	})
}

func CreateQuestionForToolCall(input CreateQuestionInteractionInput) ToolCallCommand {
	replay := replayQuestionCommand
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.createQuestionInteraction(ctx, input)
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, false)
	})
}

func CreatePoolMachineForToolCall(
	input CreatePoolMachineInput,
	completion ToolCallCompletionBuilder[CreatePoolMachineResult],
) ToolCallCommand {
	replay := replayCreateMachine(input, completion)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if completion == nil {
			return nil, errors.New("pool machine completion builder is required")
		}
		result, err := tx.createPoolMachine(ctx, input)
		if err != nil {
			return nil, err
		}
		toolCompletion, err := completion(result)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, toolCompletion); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func DeletePoolMachineForToolCall(
	input DeletePoolMachineInput,
	completion ToolCallCompletionBuilder[PoolMachineRecord],
) ToolCallCommand {
	replay := replayDeleteMachine(input, completion)
	return replayableToolCommand(replay, func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if completion == nil {
			return nil, errors.New("pool machine completion builder is required")
		}
		record, err := tx.deletePoolMachine(ctx, input)
		if err != nil {
			return nil, err
		}
		toolCompletion, err := completion(record)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, toolCompletion); err != nil {
			return nil, err
		}
		return record, nil
	})
}
