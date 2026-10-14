//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/stretchr/testify/require"
)

func seedSoftContextHistory(t *testing.T, ctx context.Context) (kernelFixture, uuid.UUID, uuid.UUID) {
	return seedSoftContextHistoryWithText(t, ctx, "OLD_HISTORY_TO_PRESERVE "+strings.Repeat("historical detail ", 800))
}

func seedSoftContextHistoryWithText(
	t *testing.T, ctx context.Context, text string,
) (kernelFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/soft-context", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10_000), MaxOutputTokens: new(1_024)})
	client := &sequenceKernelModel{
		providerModelSlug:          "soft-context",
		capabilities:               model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1_024)},
		preparedInputTokenEstimate: 500,
		responses:                  []model.Response{completeProgressiveSummaryResponse("Old history received.")},
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, text, fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	return fixture, agentID, userID
}

func uncertainSoftContextClient(responses ...model.Response) *sequenceKernelModel {
	return &sequenceKernelModel{
		providerModelSlug: "soft-context",
		capabilities:      model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1_024)},
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) {
				return 500
			}
			return 100_000
		},
		responses: responses,
	}
}

func TestAgentExecutorFailedOptionalThenRealOverflowClaimsRequiredCompaction(t *testing.T) {
	ctx := context.Background()
	fixture, agentID, userID := seedSoftContextHistory(t, ctx)
	client := uncertainSoftContextClient(
		completeProgressiveSummaryResponse("Preserve the earlier user intent and continue the current work."),
		completeProgressiveSummaryResponse("Recovered after confirmed overflow."),
	)
	client.errs = []error{
		model.ProviderError{
			Kind: model.ErrorKindTransient, Source: "test", Code: "timeout", Message: "Optional summary timed out",
		},
		model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length",
			Message: "Normal request is actually too large",
		},
	}
	work := fixture.admitContentInputTurn(
		t, ctx, agentID, userID, "Continue the current task.", fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 3, client.respondedCount())
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(3*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 4, client.respondedCount())
	require.True(t, isCompactionRequestBundle(client.responded[0].Bundle))
	require.False(t, isCompactionRequestBundle(client.responded[1].Bundle))
	require.True(t, isCompactionRequestBundle(client.responded[2].Bundle))
	require.False(t, isCompactionRequestBundle(client.responded[3].Bundle))
	var parents int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(DISTINCT parent_normal_model_call_context_id)
		FROM model_call_contexts WHERE agent_id=$1 AND operation_kind='compaction'`, agentID).Scan(&parents))
	require.Equal(t, 2, parents, "required recovery must not reuse the failed optional operation")
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}

func TestAgentExecutorOverflowWithoutCompactableHistoryEndsAfterOneCall(t *testing.T) {
	for _, kind := range []model.ErrorKind{model.ErrorKindContextWindow, model.ErrorKindPayloadTooLarge} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-recovery", fixture.Now,
				kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(32_768)})
			client := &sequenceKernelModel{
				providerModelSlug: "output-recovery", preparedInputTokenEstimate: 500,
				capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(32_768)},
				errs: []error{model.ProviderError{
					Kind: kind, Source: "test", Code: "request_capacity", Message: "Request exceeds capacity",
				}},
			}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "A request with no old source.", fixture.Now)
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Equal(t, 1, client.respondedCount())
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
		})
	}
}

type oversizedClosedHistoryKernelModel struct {
	*sequenceKernelModel
}

func (m oversizedClosedHistoryKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	prepared := m.prepared[len(m.prepared)-1]
	isSummary := isCompactionRequestBundle(prepared.Bundle)
	if (isSummary && len(request.ProviderRequest) > 12_000) ||
		(!isSummary && prepared.Bundle.ContextCheckpoint == nil) {
		m.errs = append([]error{model.ProviderError{Kind: model.ErrorKindContextWindow, Source: "test",
			Code: "context_length", Message: "Provider cannot fit the complete old source"}}, m.errs...)
	} else if isSummary {
		m.responses = append([]model.Response{
			completeProgressiveSummaryResponse("Keep the user's earlier objective; continue the current request."),
		}, m.responses...)
	} else {
		m.responses = append([]model.Response{
			completeProgressiveSummaryResponse("The agent recovered and completed the current request."),
		}, m.responses...)
	}
	return m.sequenceKernelModel.Respond(ctx, request)
}

func assertNoTerminalContextErrors(t *testing.T, ctx context.Context, fixture kernelFixture, agentID uuid.UUID) {
	t.Helper()
	var errors int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM model_outputs WHERE agent_id = $1 AND stop_reason = 'error'`, agentID,
	).Scan(&errors))
	require.Zero(t, errors)
}
