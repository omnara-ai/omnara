//go:build integration

package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorEmptyOutputRetriesAreBoundedAcrossRestarts(t *testing.T) {
	for _, tc := range []struct {
		name                string
		estimate, allowance int
		continuation        bool
	}{
		{name: "full allowance", estimate: 500, allowance: 8192},
		{name: "per-request adjustment", estimate: 3500, allowance: 5476},
		{name: "after partial output", estimate: 3500, allowance: 5476, continuation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-progress", fixture.Now,
				kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(8192)})
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "Complete the current task.", fixture.Now)
			client := &sequenceKernelModel{
				providerModelSlug: "output-progress", preparedInputTokenEstimate: tc.estimate,
				capabilities: model.Capabilities{ContextWindowTokens: 10000, MaxOutputTokens: new(8192)},
			}
			productiveCutoffs := 0
			if tc.continuation {
				client.responses = []model.Response{truncatedKernelResponse()}
				executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(time.Second))
				require.Equal(t, executionstore.ModelWorkContinue, work.Kind)
				productiveCutoffs = 1
			}
			attempts := executionstore.MaxModelCallRetriesPerOperation + 1
			for attempt := range attempts {
				response := model.Response{StopReason: model.StopReasonMaxTokens}
				if attempt%2 == 0 {
					response.Content = []model.ResponsePart{
						{Type: model.ResponsePartTypeReasoning, Text: "Unfinished internal reasoning."},
					}
				}
				client.responses = append(client.responses, response)
			}
			for attempt := range attempts {
				executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
					ModelRetryDelay: immediateKernelModelRetryDelay}
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				if attempt < attempts-1 {
					work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
						fixture.Now.Add(time.Duration(attempt+2)*time.Second))
					require.Equal(t, executionstore.ModelWorkResume, work.Kind)
				}
			}
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
			require.Equal(t, attempts+productiveCutoffs, client.respondedCount())
			for _, sent := range client.responded {
				require.Equal(t, tc.allowance, sent.Policy.MaxOutputTokens)
				require.Contains(t, string(sent.ProviderRequest), "Complete the current task.")
			}
			var retries, successfulCutoffs, terminalOutputs int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts
				WHERE agent_id=$1 AND recovery_kind='retry' AND error_code='model_output_no_progress'`,
				agentID).Scan(&retries))
			require.Equal(t, executionstore.MaxModelCallRetriesPerOperation, retries)
			require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE stop_reason='max_tokens'),
				count(*) FILTER(WHERE stop_reason='error') FROM model_outputs WHERE agent_id=$1`,
				agentID).Scan(&successfulCutoffs, &terminalOutputs))
			require.Equal(t, productiveCutoffs, successfulCutoffs, "empty output must not advance the frontier")
			require.Equal(t, 1, terminalOutputs)
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(t, ctx, agentID, userID, "A later request.", fixture.Now.Add(time.Hour))
			client.preparedInputTokenEstimate = 500
			client.responses = []model.Response{completeProgressiveSummaryResponse("The task completed.")}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
			last := client.responded[len(client.responded)-1]
			require.Equal(t, 8192, last.Policy.MaxOutputTokens)
			require.Contains(t, string(last.ProviderRequest), "A later request.")
		})
	}
}
