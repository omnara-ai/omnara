//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func createPoolMachineForTest(
	ctx context.Context,
	store *Store,
	transaction executionstore.ExecuteToolCallInput,
	input executionstore.CreatePoolMachineInput,
) (executionstore.CreatePoolMachineResult, error) {
	return executeToolCallCommandForTest[executionstore.CreatePoolMachineResult](
		ctx,
		store,
		transaction,
		executionstore.CreatePoolMachineForToolCall(input, acceptedPoolMachineCompletionForTest),
	)
}

func deletePoolMachineForTest(
	ctx context.Context,
	store *Store,
	transaction executionstore.ExecuteToolCallInput,
	input executionstore.DeletePoolMachineInput,
) (executionstore.PoolMachineRecord, error) {
	return executeToolCallCommandForTest[executionstore.PoolMachineRecord](
		ctx,
		store,
		transaction,
		executionstore.DeletePoolMachineForToolCall(input, acceptedPoolMachineCompletionForTest),
	)
}

func acceptedPoolMachineCompletionForTest[T any](T) (executionstore.ToolCallCompletionInput, error) {
	return executionstore.ToolCallCompletionInput{
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"accepted"}]`),
	}, nil
}
