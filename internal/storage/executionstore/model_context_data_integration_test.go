//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestPreparedModelContextDataMatchesWatermarkReads(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepared_data")
	ids := createToolCallBatchForProcessTest(t, ctx, f, "prepared_data", []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("first", "read_process"),
		builtInProcessToolCallBatchItem("second", "read_process"),
	})
	for _, id := range ids {
		ownedCompleteTool(t, ctx, f, id)
	}
	advance := ownedAdvanceInput(f, true)
	advance.PrepareModel = true
	work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, advance)
	require.NoError(t, err)
	require.True(t, continued)
	require.Equal(t, executionstore.ModelWorkContinue, work.Model.Kind)
	data := work.Model.Prepared.ContextData
	require.NotNil(t, data)
	events, err := f.Store.Execution().ListContextEvents(ctx, testProjectID, f.AgentID, 0, data.InputEventSequence, 500)
	require.NoError(t, err)
	require.Equal(t, events, data.Events)
	calls, err := f.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx, testProjectID, f.AgentID, 0, data.InputEventSequence,
	)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, calls, data.ToolCalls)
	ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "later")
	frozen, err := f.Store.Execution().ListContextEvents(ctx, testProjectID, f.AgentID, 0, data.InputEventSequence, 500)
	require.NoError(t, err)
	require.Equal(t, events, frozen)
}
