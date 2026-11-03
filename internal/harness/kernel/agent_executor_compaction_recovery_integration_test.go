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

func seedCompactionHistory(t *testing.T, ctx context.Context) (kernelFixture, uuid.UUID, uuid.UUID) {
	return seedCompactionHistoryWithText(t, ctx, "OLD_HISTORY_TO_PRESERVE "+strings.Repeat("historical detail ", 800))
}

func seedCompactionHistoryWithText(
	t *testing.T, ctx context.Context, text string,
) (kernelFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/compaction-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10_000), MaxOutputTokens: new(1_024)})
	client := &sequenceKernelModel{
		providerModelSlug:          "compaction-recovery",
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

func compactionRecoveryClient(responses ...model.Response) *sequenceKernelModel {
	return &sequenceKernelModel{
		providerModelSlug: "compaction-recovery",
		capabilities:      model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1_024)},
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) || bundle.ContextCheckpoint != nil {
				return 500
			}
			return 100_000
		},
		responses: responses,
	}
}

func TestAgentExecutorCompactionTriggersShareRetryAndCompletionFlow(t *testing.T) {
	for _, providerOverflow := range []bool{false, true} {
		name := "local input budget"
		if providerOverflow {
			name = "provider overflow"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			fixture, agentID, userID := seedCompactionHistory(t, ctx)
			client := compactionRecoveryClient(
				completeProgressiveSummaryResponse("Preserve the earlier user intent and continue the current work."),
				completeProgressiveSummaryResponse("Recovered after compaction."),
			)
			client.errs = []error{model.ProviderError{
				Kind: model.ErrorKindTransient, Source: "test", Code: "timeout", Message: "Summary timed out",
			}}
			normalAttempts := 0
			if providerOverflow {
				normalAttempts = 1
				client.preparedInputTokenEstimator = nil
				client.preparedInputTokenEstimate = 500
				client.errs = append([]error{model.ProviderError{
					Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length",
					Message: "Normal request is too large",
				}}, client.errs...)
			}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
				"Continue the current task.", fixture.Now.Add(time.Second))
			executor := AgentExecutor{
				Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				ModelRetryDelay: immediateKernelModelRetryDelay,
				OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
					t.Error("retryable compaction must not notify failure")
					return nil
				},
			}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, normalAttempts+1)
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, normalAttempts+2)
			for _, request := range client.responded[normalAttempts:] {
				require.True(t, isCompactionRequestBundle(request.Bundle), "failed summary must retry before normal work")
			}
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(3*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, normalAttempts+3)
			final := client.responded[normalAttempts+2]
			require.False(t, isCompactionRequestBundle(final.Bundle))
			require.NotNil(t, final.Bundle.ContextCheckpoint)
			assertNoTerminalContextErrors(t, ctx, fixture, agentID)
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
		})
	}
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
