//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/stretchr/testify/require"
)

type outputRestorationKernelModel struct {
	*sequenceKernelModel
	acceptedReducedAllowance  int
	noProgressAtFullAllowance bool
	summaryCalls              int
}

func (m *outputRestorationKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	prepared := m.prepared[len(m.prepared)-1]
	response := completeProgressiveSummaryResponse("The current task completed.")
	var failure error
	switch {
	case isCompactionRequestBundle(prepared.Bundle):
		m.summaryCalls++
		response = completeProgressiveSummaryResponse(strings.Repeat("Preserve the established task state. ", 80))
		response.ID = "restored-output-summary"
	case prepared.Policy.MaxOutputTokens < 8_192:
		if prepared.Policy.MaxOutputTokens <= m.acceptedReducedAllowance {
			response = outputRestorationNoProgress()
		} else {
			failure = outputRestorationOverflow()
		}
	case prepared.Bundle.ContextCheckpoint != nil && len(prepared.Bundle.ContextCheckpoint.Summary) > 1_000:
		failure = outputRestorationOverflow()
	case m.noProgressAtFullAllowance:
		response = outputRestorationNoProgress()
	}
	if failure != nil {
		m.errs = append([]error{failure}, m.errs...)
	} else {
		m.responses = append([]model.Response{response}, m.responses...)
	}
	return m.sequenceKernelModel.Respond(ctx, request)
}

func outputRestorationOverflow() error {
	return model.ProviderError{Kind: model.ErrorKindContextWindow, Source: "test",
		Code: "request_capacity", Message: "Input plus output exceeds capacity"}
}

func outputRestorationNoProgress() model.Response {
	return model.Response{StopReason: model.StopReasonMaxTokens,
		Content: []model.ResponsePart{{Type: model.ResponsePartTypeReasoning, Text: "Unfinished internal reasoning."}}}
}

func newOutputRestorationClient(acceptedReducedAllowance int) *outputRestorationKernelModel {
	return &outputRestorationKernelModel{
		sequenceKernelModel: &sequenceKernelModel{
			providerModelSlug: "soft-context",
			capabilities:      model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(8_192)},
			preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
				if isCompactionRequestBundle(bundle) {
					return 500
				}
				return 30_000
			},
		},
		acceptedReducedAllowance: acceptedReducedAllowance,
	}
}

func TestAgentExecutorRestoresOutputOnceAndReducesHistoryUntilUsefulResponse(t *testing.T) {
	for _, acceptedReducedAllowance := range []int{2_048, 5_000} {
		name := "provider overflow before reduction"
		if acceptedReducedAllowance == 5_000 {
			name = "estimate clamp without provider overflow"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			const current = "CURRENT_REQUEST_SURVIVES_OUTPUT_RESTORATION"
			original := strings.Repeat("full saved task history ", 2_000)
			fixture, work, prior := seedCheckpointBeforeCurrentAttempt(t, ctx, original, current)
			client := newOutputRestorationClient(acceptedReducedAllowance)
			for attempt := range 30 {
				executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
					ModelRetryDelay: immediateKernelModelRetryDelay}
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				if pendingModelWork(t, ctx, fixture, work.AgentID) == 0 {
					break
				}
				work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
					fixture.Now.Add(time.Duration(attempt+3)*time.Second))
			}
			require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
			require.Equal(t, 1, client.summaryCalls)
			assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
			var restorationCount, reducedBeforeRestore int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT
				count(*) FILTER(WHERE recovery_kind='restore_output'),
				count(*) FILTER(WHERE recovery_max_output_tokens IS NOT NULL)
				FROM model_call_contexts WHERE agent_id=$1`, work.AgentID).Scan(&restorationCount, &reducedBeforeRestore))
			require.Equal(t, 1, restorationCount)
			if acceptedReducedAllowance == 5_000 {
				require.Zero(t, reducedBeforeRestore, "the sent estimate clamp alone must qualify for restoration")
			} else {
				require.Positive(t, reducedBeforeRestore)
			}
			var restored, sawCheckpoint, sawExcerpt bool
			for _, sent := range client.responded {
				if isCompactionRequestBundle(sent.Bundle) {
					require.True(t, restored)
					require.NotContains(t, string(sent.ProviderRequest), current)
					continue
				}
				if sent.Policy.MaxOutputTokens == 8_192 {
					restored = true
				}
				if restored {
					require.Equal(t, 8_192, sent.Policy.MaxOutputTokens,
						"restored requests must not be clamped or halved again after a checkpoint or excerpt")
				}
				require.Equal(t, client.responded[0].Bundle.Messages, sent.Bundle.Messages)
				require.Contains(t, string(sent.ProviderRequest), current)
				sawCheckpoint = sawCheckpoint || sent.Bundle.ContextCheckpoint.ID != prior.ID.String()
				sawExcerpt = sawExcerpt || strings.Contains(sent.Bundle.ContextCheckpoint.Summary, "Earlier history excerpted")
			}
			require.True(t, restored)
			require.True(t, sawCheckpoint)
			require.True(t, sawExcerpt)
			stored, found, err := fixture.Store.Execution().GetContextCheckpoint(
				ctx, kernelTestProjectID, work.AgentID, prior.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, original, stored.Summary)
		})
	}
}

func TestAgentExecutorRestoredOutputWithoutProgressStopsAndLaterInputRecovers(t *testing.T) {
	ctx := context.Background()
	fixture, work, _ := seedCheckpointBeforeCurrentAttempt(t, ctx,
		strings.Repeat("earlier saved history ", 2_000), "Complete the current task.")
	client := newOutputRestorationClient(5_000)
	client.noProgressAtFullAllowance = true
	for attempt := range 40 {
		executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
			ModelRetryDelay: immediateKernelModelRetryDelay}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		if pendingModelWork(t, ctx, fixture, work.AgentID) == 0 {
			break
		}
		work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
			fixture.Now.Add(time.Duration(attempt+3)*time.Second))
	}
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	require.Less(t, len(client.responded), 20)
	var restored, retries, emptySuccessfulOutputs, terminalOutputs int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER(WHERE recovery_kind='restore_output'),
		count(*) FILTER(WHERE recovery_kind='retry' AND error_code='output_recovery_no_progress')
		FROM model_call_contexts WHERE agent_id=$1`, work.AgentID).Scan(&restored, &retries))
	require.Equal(t, 1, restored)
	require.Equal(t, 8, retries)
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE stop_reason='max_tokens'),
		count(*) FILTER(WHERE stop_reason='error') FROM model_outputs WHERE agent_id=$1`, work.AgentID).Scan(
		&emptySuccessfulOutputs, &terminalOutputs))
	require.Zero(t, emptySuccessfulOutputs)
	require.Equal(t, 1, terminalOutputs)
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitContentInputTurn(t, ctx, work.AgentID, kernelTestUserID,
		"A later request with a usable model response.", fixture.Now.Add(time.Hour))
	client.noProgressAtFullAllowance = false
	client.preparedInputTokenEstimator = func(modelcontext.Bundle) int { return 500 }
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	require.Contains(t, string(client.responded[len(client.responded)-1].ProviderRequest), "A later request")
}
