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
	work := reclaimAgentWork(t, ctx, &f)
	require.Equal(t, executionstore.ModelWorkContinue, work.Model.Kind)
	prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
		OpeningInputIDs:          work.Model.InputIDs,
		SourceModelCallContextID: work.Model.SourceModelCallContextID,
		SourceModelOutputID:      work.Model.SourceModelOutputID,
	})
	require.NoError(t, err)
	data := prepared.ContextData
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
