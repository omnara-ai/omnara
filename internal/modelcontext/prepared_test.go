package modelcontext

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestPreparedContextMatchesStoredTranscriptAndCheckpointOverride(t *testing.T) {
	ctx := t.Context()
	for _, checkpoint := range []bool{false, true} {
		name := "full transcript"
		if checkpoint {
			name = "checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			store := &fakeContextStore{watermark: 602}
			for i := 1; i <= 601; i++ {
				store.messages = append(store.messages, contextTextEvent(t, testIDN(i), int64(i), "message"))
			}
			after := int64(0)
			if checkpoint {
				after = 42
				store.checkpoints = []executionstore.ContextCheckpointRecord{{ID: testIDN(700), Summary: "Earlier work",
					SummarizedThroughEventSequence: after, CheckpointEventSequence: 43}}
				store.outputLimitBoundaries = map[int64]bool{after: true}
			}
			store.toolCalls = []executionstore.ToolCallRecord{{
				ID: testIDN(800), ProviderCallID: "tool", TurnID: testTurnID, ModelCallContextID: testIDN(801),
				ToolCallResultID: testIDN(802), ToolResultEventID: testIDN(803), Name: "list_agents", Input: []byte(`{}`),
				SourceEventSequence: 601, ToolResultEventSequence: 602, State: executionstore.ToolCallStateCompleted,
				Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: []byte(`[{"type":"text","text":"done"}]`),
			}}
			snapshot, err := store.CaptureAgentConfigForModelContext(ctx, testProjectID, testAgentID)
			require.NoError(t, err)
			data := &executionstore.ModelContextData{ProjectID: testProjectID, AgentID: testAgentID,
				InputEventSequence: 602, Events: store.messages[int(after):], ToolCalls: store.toolCalls}
			if checkpoint {
				data.Checkpoint = &store.checkpoints[0]
				data.CheckpointEndsWithOutputLimit = true
			}
			input := BuildInput{ProjectID: testProjectID, AgentID: testAgentID, TurnID: testTurnID,
				OpeningInputIDs: []uuid.UUID{testInputID}, Now: time.Now(), AgentConfigSnapshot: &snapshot}
			builder := Builder{Store: store}
			expected, err := builder.Build(ctx, input)
			require.NoError(t, err)
			input.ContextData = data
			prepared, err := builder.Build(ctx, input)
			require.NoError(t, err)
			require.Equal(t, expected, prepared)
			require.Len(t, prepared.Messages, 601-int(after))
			input.CheckpointOverride = &CheckpointRef{Summary: "Candidate", SummarizedThroughEventSequence: 500}
			prepared, err = builder.Build(ctx, input)
			require.NoError(t, err)
			input.ContextData = nil
			expected, err = builder.Build(ctx, input)
			require.NoError(t, err)
			require.Equal(t, expected, prepared)
			input.ContextData = data
			input.CheckpointOverride = nil
			data.InputEventSequence++
			_, err = builder.Build(ctx, input)
			require.ErrorContains(t, err, "does not match")
		})
	}
}
