//go:build integration

package executionstore_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestToolInputIngressKeepsRawHistoryAndTerminalMalformedCalls(t *testing.T) {
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "strict_tool_input")
	providerModelSlug := modelProviderSlugForContext(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	// Every key is business data. Ingress preserves these values in raw history;
	// execution validates them against the runtime tool schema.
	raw := json.RawMessage(`{"count":9007199254740993,"decimal":1.2300,"omnara_channel":"literal","nested":{"omnara_channel":true}}`)
	ambiguous := `{"outer":[{"a":1,"\u0061":2}]}`
	parts := []model.ResponsePart{
		model.NewToolCallPart("accepted", "lookup", raw),
		model.NewToolCallPart("duplicate", "lookup", json.RawMessage(ambiguous)),
		model.NewToolCallPart("trailing", "lookup", json.RawMessage(`{} {}`)),
	}
	// Native string-encoded provider replay stays untouched by argument parsing.
	providerReplay, err := json.Marshal(map[string]string{"arguments": ambiguous})
	require.NoError(t, err)
	envelope, err := model.NewResponseEnvelopeForStorage(
		providerModelSlug, modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "strict-input", StopReason: model.StopReasonToolUse, ProviderReplay: providerReplay, Content: parts,
		},
	)
	require.NoError(t, err)
	input := executionstore.RecordToolCallSourceAndCompleteContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, ModelCallContextID: claim.Context.ID,
		ProviderResponse: envelope,
		ToolCallBindings: []executionstore.ToolCallBindingInput{
			{ProviderCallID: "accepted", Type: toolcatalog.ToolTypeCustom},
			{ProviderCallID: "duplicate", Type: toolcatalog.ToolTypeCustom},
			{ProviderCallID: "trailing", Type: toolcatalog.ToolTypeCustom},
		},
	}
	// Direct envelope callers cannot bypass parsing and let JSONB erase a
	// duplicate. Rejection must leave the context available for valid recording.
	bypass := input
	bypass.ProviderResponse.Normalized.Content = slices.Clone(envelope.Normalized.Content)
	bypass.ProviderResponse.Normalized.Content[0].ToolInput = json.RawMessage(ambiguous)
	_, _, err = fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx, bypass)
	require.ErrorContains(t, err, "/outer/0/a")
	_, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.False(t, found)

	event, calls, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx, input)
	require.NoError(t, err)
	require.Len(t, calls, 3)
	require.Equal(t, executionstore.ToolCallStateAwaitingAuthorization, calls[0].State)
	require.True(t, jsoncanonical.Equal(raw, calls[0].Input))
	var saved map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(calls[0].Input, &saved))
	require.Equal(t, json.RawMessage(`9007199254740993`), saved["count"])
	require.Equal(t, json.RawMessage(`"literal"`), saved["omnara_channel"])
	for i := 1; i < len(calls); i++ {
		require.Equal(t, executionstore.ToolCallStateCompleted, calls[i].State)
		require.Equal(t, executionstore.ToolResultOutcomeFailed, calls[i].Outcome)
		require.Equal(t, json.RawMessage(`{}`), calls[i].Input)
		require.Contains(t, string(calls[i].ResultContentParts), parts[i].ToolCallError)
		require.Contains(t, string(calls[i].ResultContentParts), `"malformed"`)
		_, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
			ctx, testProjectID, fixture.AgentID, calls[i].ID, executionstore.AgentInteractionKindPermission,
		)
		require.NoError(t, err)
		require.False(t, found)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, jsoncanonical.Equal(providerReplay, output.ProviderReplay))
	rows, err := fixture.Store.Execution().ListCompactionSourceEvents(ctx, testProjectID, fixture.AgentID, 0, 100)
	require.NoError(t, err)
	var foundHistory bool
	for _, row := range rows {
		if row.Kind == string(events.KindModelOutput) && row.Sequence == event.Sequence {
			foundHistory = true
			require.Contains(t, string(row.ContentParts), `9007199254740993`)
			require.Contains(t, string(row.ContentParts), `"omnara_channel"`)
			require.Contains(t, string(row.ContentParts), `"literal"`)
		}
	}
	require.True(t, foundHistory)
	replay, replayedCalls, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx, input)
	require.NoError(t, err)
	require.Equal(t, event.ID, replay.ID)
	require.Len(t, replayedCalls, len(calls))
	for i, call := range calls {
		replayedCall := replayedCalls[i]
		if len(call.ResultContentParts) == 0 || len(replayedCall.ResultContentParts) == 0 {
			require.Equal(t, call.ResultContentParts, replayedCall.ResultContentParts)
		} else {
			require.True(t, jsoncanonical.Equal(call.ResultContentParts, replayedCall.ResultContentParts),
				"result JSON must survive JSONB formatting")
		}
		call.ResultContentParts, replayedCall.ResultContentParts = nil, nil
		require.Equal(t, call, replayedCall, "replay must preserve identity, arguments and state")
	}
	changed := input
	changed.ProviderResponse.Normalized.Content = slices.Clone(envelope.Normalized.Content)
	changed.ProviderResponse.Normalized.Content[0].ToolInput = json.RawMessage(`{"count":9007199254740992,"decimal":1.2300,"omnara_channel":"literal","nested":{"omnara_channel":true}}`)
	_, _, err = fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx, changed)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
}
