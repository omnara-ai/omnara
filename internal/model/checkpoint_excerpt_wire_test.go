package model_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestCheckpointExcerptChangesWireInputAndInvalidatesPriorMeasurement(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1024}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, inputIdentityScope: "route"}) {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			summary := "SAVED_HEAD " + strings.Repeat("older context ", 1000) + " SAVED_TAIL"
			bundle := modelcontext.Bundle{
				SystemPrompt: "Follow the current request.",
				ContextCheckpoint: &modelcontext.CheckpointRef{
					ID: id.String(), Summary: summary, SummarizedThroughEventSequence: 5,
				},
				Messages: []modelcontext.Message{{
					ID: "prior-question", Role: modelprotocol.RoleUser, Sequence: 6,
					Content: json.RawMessage(`[{"type":"text","text":"prior question"}]`),
				}},
			}
			prepare := func(contextBundle modelcontext.Bundle) model.PreparedRequest {
				t.Helper()
				prepared, err := model.PrepareForSend(context.Background(), tc.client, model.PrepareForSendInput{
					Context: contextBundle, Policy: model.RequestPolicyFromCapabilities(caps),
				})
				require.NoError(t, err)
				require.NotNil(t, prepared.RequestInputIdentity)
				return prepared
			}
			first := prepare(bundle)
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				ID: "answer", Role: modelprotocol.RoleAssistant, Sequence: 7,
				Content:              json.RawMessage(`[{"type":"text","text":"prior answer"}]`),
				Usage:                modelenvelope.Usage{InputTokens: 8000, OutputTokens: 20},
				RequestInputIdentity: first.RequestInputIdentity, ServedProviderModelSlug: "test-model",
				StopReason: modelenvelope.StopReasonEndTurn,
			}, modelcontext.Message{
				ID: "current", Role: modelprotocol.RoleUser, Sequence: 8,
				Content: json.RawMessage(`[{"type":"text","text":"CURRENT_REQUEST_UNCHANGED"}]`),
			})
			full := prepare(bundle)
			require.True(t, full.HasMeasuredInputPrefix)
			previousBytes := len(full.Body)
			for _, retained := range []int{1024, 256, 0} {
				projected, err := modelcontext.ApplyCheckpointExcerpt(bundle, id, retained)
				require.NoError(t, err)
				prepared := prepare(projected)
				require.False(t, prepared.HasMeasuredInputPrefix)
				require.NotEqual(t, full.RequestInputIdentity.Fingerprint, prepared.RequestInputIdentity.Fingerprint)
				require.Less(t, len(prepared.Body), previousBytes)
				require.Contains(t, string(prepared.Body), "CURRENT_REQUEST_UNCHANGED")
				require.Contains(t, string(prepared.Body), "remains stored")
				require.Equal(t, summary, bundle.ContextCheckpoint.Summary)
				previousBytes = len(prepared.Body)
			}
		})
	}
}
