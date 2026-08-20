package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type toolCallCommandFunc func(context.Context, *toolCallTransaction) (any, error)

func (f toolCallCommandFunc) apply(ctx context.Context, tx *toolCallTransaction) (any, error) {
	return f(ctx, tx)
}

type ToolCallCompletionBuilder[T any] func(T) (ToolCallCompletionInput, error)

func CompleteToolCallInExecution(input ToolCallCompletionInput) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		return tx.completeToolCall(ctx, input)
	})
}

func StartToolCallAsync() ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		return nil, tx.startToolCall(ctx, true)
	})
}

func StartProcessForToolCall(input CreateProcessInput) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.startProcess(ctx, input)
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, false)
	})
}

func CreateProcessActionForToolCall(input CreateProcessActionInput) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
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
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.createProcessAction(ctx, input)
		if errors.Is(err, storeerr.ErrProcessAlreadyStopped) {
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
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.createQuestionInteraction(ctx, input)
		if err != nil {
			return nil, err
		}
		return record, tx.startToolCall(ctx, true)
	})
}

func CreatePoolMachineForToolCall(
	input CreatePoolMachineInput,
	completion ToolCallCompletionBuilder[CreatePoolMachineResult],
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
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
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
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

func SetIntegrationTargetForToolCall(
	integrationTargetID ID,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		record, err := tx.setAgentIntegrationTarget(ctx, integrationTargetID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return record, nil
	})
}
