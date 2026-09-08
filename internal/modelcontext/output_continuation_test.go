package modelcontext

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestOutputLimitNoticePreservesHistoricalPrefix(t *testing.T) {
	for _, content := range []string{
		`[{"type":"text","text":"partial"}]`,
		`[{"type":"reasoning","text":"thinking"}]`,
		`[]`,
	} {
		t.Run(content, func(t *testing.T) {
			for _, reason := range []modelenvelope.StopReason{
				modelenvelope.StopReasonMaxTokens, modelenvelope.StopReasonEndTurn,
			} {
				messages, err := contextEventsToMessages([]executionstore.ContextEventRecord{{
					ID: testIDN(9100), ModelCallContextID: testIDN(9101), Sequence: 42,
					Role: modelprotocol.RoleAssistant, StopReason: reason,
					ContentParts: json.RawMessage(content), ProviderReplay: json.RawMessage(`{"opaque":"unchanged"}`),
				}})
				require.NoError(t, err)
				require.Len(t, messages, 1)
				require.JSONEq(t, content, string(messages[0].Content))
				require.Equal(t, reason, messages[0].StopReason)
				bundle := Bundle{Messages: messages}
				history, err := CanonicalHistory(bundle)
				require.NoError(t, err)
				require.Equal(t, messages[0], history[0].Message)
				if reason == modelenvelope.StopReasonMaxTokens {
					require.Len(t, history, 2)
					notice := history[1].Message
					require.Equal(t, modelprotocol.RoleUser, notice.Role)
					require.Contains(t, string(notice.Content), outputLimitNotice)
					require.Empty(t, notice.ID)
					require.Empty(t, notice.ModelCallContextID)
					require.Empty(t, notice.ProviderReplay)
				} else {
					require.Len(t, history, 1)
				}
				bundle.Messages = append(bundle.Messages,
					Message{ID: "later-user", Sequence: 43, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"new input"}]`)},
					Message{ID: "later-output", Sequence: 44, Role: modelprotocol.RoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"done"}]`)},
				)
				later, err := CanonicalHistory(bundle)
				require.NoError(t, err)
				require.Equal(t, history, later[:len(history)], "reasoning replay prefixes must keep historical notices")
			}
		})
	}
}

func TestCheckpointOutputLimitNoticeUsesFixedBoundary(t *testing.T) {
	for _, override := range []bool{false, true} {
		for _, cutoff := range []bool{false, true} {
			store := &fakeContextStore{
				watermark:             43,
				outputLimitBoundaries: map[int64]bool{42: cutoff},
				checkpoints: []executionstore.ContextCheckpointRecord{{
					ID: testIDN(9100), Summary: "Earlier work", SummarizedThroughEventSequence: 42, CheckpointEventSequence: 43,
				}},
			}
			input := BuildInput{
				ProjectID: testProjectID, AgentID: testAgentID, TurnID: testTurnID,
				OpeningInputIDs: []storage.ID{testInputID}, Now: time.Now(),
			}
			if override {
				input.CheckpointOverride = &CheckpointRef{
					Summary: "Earlier work", SummarizedThroughEventSequence: 42,
					EndsWithOutputLimit: !cutoff, // The builder derives this from storage.
				}
			}
			bundle, err := (Builder{Store: store}).Build(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, cutoff, bundle.ContextCheckpoint.EndsWithOutputLimit)
			projected := ProjectedCheckpointContent(*bundle.ContextCheckpoint)
			if cutoff {
				require.Contains(t, projected, "</context_checkpoint>\n\n"+outputLimitNotice)
			} else {
				require.NotContains(t, projected, outputLimitNotice)
			}
			store.watermark = 44
			store.messages = append(store.messages, contextTextEvent(t, testIDN(9102), 44, "later input"))
			later, err := (Builder{Store: store}).Build(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, projected, ProjectedCheckpointContent(*later.ContextCheckpoint))
		}
	}
}
