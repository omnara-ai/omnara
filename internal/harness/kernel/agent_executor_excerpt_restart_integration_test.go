//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

type transientExcerptKernelModel struct {
	*sequenceKernelModel
	respond              func(context.Context, model.Request) (model.Response, error)
	failedExcerptRequest json.RawMessage
}

func (m *transientExcerptKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	if m.failedExcerptRequest == nil &&
		strings.Contains(string(request.ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT") {
		m.failedExcerptRequest = append(json.RawMessage(nil), request.ProviderRequest...)
		m.errs = append([]error{model.ProviderError{
			Kind: model.ErrorKindTransient, Source: "test", Code: "unavailable", Message: "Summary service unavailable",
		}}, m.errs...)
		return m.sequenceKernelModel.Respond(ctx, request)
	}
	return m.respond(ctx, request)
}

func TestAgentExecutorResumesPersistedExcerptAfterTransientFailureOnNewLease(t *testing.T) {
	for _, checkpointOnly := range []bool{false, true} {
		name := "old message"
		if checkpointOnly {
			name = "prior checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			const current = "CURRENT_REQUEST_RETAINED_DURING_RESTART"
			original := "OLD_HEAD " + strings.Repeat("closed history remains stored ", 2_000) + " OLD_TAIL"
			var fixture kernelFixture
			var work ModelWorkExecution
			client := &transientExcerptKernelModel{}
			if checkpointOnly {
				fixture, work, _ = seedCheckpointBeforeCurrentAttempt(t, ctx, original, current)
				base := checkpointRecompressionKernelModel{
					sequenceKernelModel: &sequenceKernelModel{
						providerModelSlug: "soft-context", preparedInputTokenEstimate: 500,
						capabilities: model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1024)},
					},
					originalSummary: original,
				}
				client.sequenceKernelModel, client.respond = base.sequenceKernelModel, base.Respond
			} else {
				var agentID, userID uuid.UUID
				fixture, agentID, userID = seedSoftContextHistoryWithText(t, ctx, original)
				work = fixture.admitContentInputTurn(t, ctx, agentID, userID, current, fixture.Now.Add(time.Second))
				base := oversizedClosedHistoryKernelModel{uncertainSoftContextClient()}
				client.sequenceKernelModel, client.respond = base.sequenceKernelModel, base.Respond
			}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				ModelRetryDelay: immediateKernelModelRetryDelay}
			for attempt := range 20 {
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				if client.failedExcerptRequest != nil {
					break
				}
				work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
					fixture.Now.Add(time.Duration(attempt+3)*time.Second))
			}
			require.NotNil(t, client.failedExcerptRequest)
			var failedContextID uuid.UUID
			var excerptBytes int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT id,source_excerpt_bytes FROM model_call_contexts
				WHERE agent_id=$1 AND operation_kind='compaction' AND recovery_kind='retry'
				ORDER BY created_at DESC LIMIT 1`, work.AgentID).Scan(&failedContextID, &excerptBytes))
			require.Positive(t, excerptBytes)
			requestCount := client.respondedCount()
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(time.Minute))
			require.Equal(t, failedContextID, work.ModelCallContextID)
			executor = AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				ModelRetryDelay: immediateKernelModelRetryDelay}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Greater(t, client.respondedCount(), requestCount)
			require.Equal(t, string(client.failedExcerptRequest), string(client.responded[requestCount].ProviderRequest))
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Minute))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			final := client.responded[len(client.responded)-1]
			require.False(t, isCompactionRequestBundle(final.Bundle))
			require.Contains(t, string(final.ProviderRequest), current)
			require.Contains(t, final.Bundle.ContextCheckpoint.Summary, "omitted")
			require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
			assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
		})
	}
}
