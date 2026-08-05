package storagetest

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func ExecuteToolCallCommand[T any](
	ctx context.Context,
	store *storage.Store,
	input executionstore.ExecuteToolCallInput,
	command executionstore.ToolCallCommand,
) (T, error) {
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		input,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return command, nil
		},
	)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := execution.CommandResult.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("tool call command returned %T", execution.CommandResult)
	}
	return result, nil
}

func StartProcessForToolCall(
	ctx context.Context,
	store *storage.Store,
	executionInput executionstore.ExecuteToolCallInput,
	processInput executionstore.CreateProcessInput,
) (executionstore.ProcessRecord, error) {
	return ExecuteToolCallCommand[executionstore.ProcessRecord](
		ctx,
		store,
		executionInput,
		executionstore.StartProcessForToolCall(processInput),
	)
}

func CreateProcessActionForToolCall(
	ctx context.Context,
	store *storage.Store,
	executionInput executionstore.ExecuteToolCallInput,
	actionInput executionstore.CreateProcessActionInput,
) (executionstore.ProcessActionRecord, error) {
	return ExecuteToolCallCommand[executionstore.ProcessActionRecord](
		ctx,
		store,
		executionInput,
		executionstore.CreateProcessActionForToolCall(actionInput),
	)
}
