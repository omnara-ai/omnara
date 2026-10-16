//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestPreparedModelCallTranscriptIgnoresLaterEvents(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepared_watermark")
	ids := createToolCallBatchForProcessTest(t, ctx, f, "prepared_watermark", []processToolCallBatchItem{
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
	require.True(t, prepared.Claim.Claimed)
	watermark := prepared.Claim.Context.InputEventSequence
	read := func() ([]executionstore.ContextEventRecord, []executionstore.ToolCallRecord) {
		events, err := f.Store.Execution().ListContextEvents(ctx, testProjectID, f.AgentID, 0, watermark, 500)
		require.NoError(t, err)
		calls, err := f.Store.Execution().ListCompletedToolCallsAtWatermark(ctx, testProjectID, f.AgentID, 0, watermark)
		require.NoError(t, err)
		return events, calls
	}
	events, calls := read()
	require.NotEmpty(t, events)
	require.Len(t, calls, 2)
	_, err = f.Store.Execution().RecordModelOutputAndCompleteContext(ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
			ModelCallContextID: prepared.Claim.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: modelProviderSlugForContext(
					t, ctx, f.Store, testProjectID, f.AgentID, prepared.Claim.Context.ID,
				),
				APIFormat:  modelprotocol.APIFormatOpenAIResponses,
				APIVariant: modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_after_watermark",
					Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "later output"}},
					StopReason: modelenvelope.StopReasonEndTurn,
				},
			},
		})
	require.NoError(t, err)
	latest, err := f.Store.Execution().MaxEventSequence(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Greater(t, latest, watermark)
	laterEvents, laterCalls := read()
	require.Equal(t, events, laterEvents)
	require.Equal(t, calls, laterCalls)
}
