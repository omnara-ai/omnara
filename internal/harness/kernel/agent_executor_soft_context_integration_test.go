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

func TestAgentExecutorUncertainOversizedOpeningStillReachesProvider(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/soft-context", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10_000), MaxOutputTokens: new(1_024)})
	client := &sequenceKernelModel{
		providerModelSlug:          "soft-context",
		capabilities:               model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1_024)},
		preparedInputTokenEstimate: 100_000,
		responses: []model.Response{
			completeProgressiveSummaryResponse("Provider accepted the input."),
		},
	}
	work := fixture.admitContentInputTurn(
		t, ctx, agentID, userID, strings.Repeat("unfamiliar tokenization ", 3_000), fixture.Now,
	)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	require.False(t, isCompactionRequestBundle(client.responded[0].Bundle))
	require.Contains(t, string(client.responded[0].ProviderRequest), strings.Repeat("unfamiliar tokenization ", 3_000))
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}

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

func TestAgentExecutorOptionalCompactionFailureResumesNormalAcrossLease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  error
		response model.Response
	}{
		{name: "timeout", failure: model.ProviderError{
			Kind: model.ErrorKindTransient, Source: "test", Code: "timeout", Message: "summary timed out",
		}},
		{name: "refusal", response: model.Response{ID: "summary-refusal", StopReason: model.StopReasonRefusal,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Cannot summarize."}}}},
		{name: "truncated", response: model.Response{ID: "summary-truncated", StopReason: model.StopReasonMaxTokens,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Incomplete summary."}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture, agentID, userID := seedSoftContextHistory(t, ctx)
			responses := []model.Response{}
			if tc.failure == nil {
				responses = append(responses, tc.response)
			}
			responses = append(responses, completeProgressiveSummaryResponse("Normal request recovered."),
				completeProgressiveSummaryResponse("Later turn kept working."))
			client := uncertainSoftContextClient(responses...)
			if tc.failure != nil {
				client.errs = []error{tc.failure}
			}
			work := fixture.admitContentInputTurn(
				t, ctx, agentID, userID, "CURRENT_REQUEST_RETAINED", fixture.Now.Add(time.Second),
			)
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				StreamPublisher: &capturingStreamPublisher{}}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Equal(t, 1, client.respondedCount(), "optional maintenance gets only one model call")
			require.True(t, isCompactionRequestBundle(client.responded[0].Bundle))

			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
			executor = AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				StreamPublisher: &capturingStreamPublisher{}}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Equal(t, 2, client.respondedCount())
			require.False(t, isCompactionRequestBundle(client.responded[1].Bundle))
			require.Contains(t, string(client.responded[1].ProviderRequest), "OLD_HISTORY_TO_PRESERVE")
			require.Contains(t, string(client.responded[1].ProviderRequest), "CURRENT_REQUEST_RETAINED")
			require.True(t, client.respondHadSink[1], "first actual normal request must still stream")
			assertNoTerminalContextErrors(t, ctx, fixture, agentID)

			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(
				t, ctx, agentID, userID, "A later request", fixture.Now.Add(3*time.Second),
			)
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Equal(t, 3, client.respondedCount())
			require.False(t, isCompactionRequestBundle(client.responded[2].Bundle),
				"a successful answer without measured headroom must not rearm the same pessimistic estimate")
			assertNoTerminalContextErrors(t, ctx, fixture, agentID)
		})
	}
}

func TestAgentExecutorCheckpointGetsNormalAttemptDespiteUncertainEstimate(t *testing.T) {
	ctx := context.Background()
	fixture, agentID, userID := seedSoftContextHistory(t, ctx)
	client := uncertainSoftContextClient(
		completeProgressiveSummaryResponse("Keep the user's original intent and continue the current task."),
		completeProgressiveSummaryResponse("Continued using the checkpoint."))
	work := fixture.admitContentInputTurn(
		t, ctx, agentID, userID, "Continue using the earlier history.", fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 2, client.respondedCount())
	require.False(t, isCompactionRequestBundle(client.responded[1].Bundle))
	require.NotNil(t, client.responded[1].Bundle.ContextCheckpoint)
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
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

type minimumOutputKernelModel struct {
	*sequenceKernelModel
	minimum int
}

func (m minimumOutputKernelModel) OutputTokenLimits() (model.OutputTokenLimits, error) {
	return model.OutputTokenLimits{Minimum: m.minimum}, nil
}

func TestAgentExecutorOutputRecoveryHonorsMinimumAndBoundsFailures(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(32_768)})
	client := minimumOutputKernelModel{
		sequenceKernelModel: &sequenceKernelModel{
			providerModelSlug: "output-recovery", preparedInputTokenEstimate: 500,
			capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(32_768)},
		},
		minimum: 2_049,
	}
	for range 5 {
		client.errs = append(client.errs, model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length", Message: "Input still too large",
		})
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "Try this request.", fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	for i := range 5 {
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		if i < 4 {
			work = continueTurnOnNewLeaseForKernelTest(
				t, ctx, fixture, work, fixture.Now.Add(time.Duration(i+1)*time.Second),
			)
		}
	}
	require.Equal(t, 5, client.respondedCount())
	for i, allowance := range []int{32_768, 16_384, 8_192, 4_096, 2_049} {
		require.Equal(t, allowance, client.responded[i].Policy.MaxOutputTokens)
	}
	require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
}

func TestAgentExecutorReducedOutputWithoutProgressUsesBoundedRetry(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(32_768)})
	client := &sequenceKernelModel{
		providerModelSlug: "output-recovery", preparedInputTokenEstimate: 500,
		capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(32_768)},
		errs: []error{model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length",
			Message: "Input plus output exceeds capacity",
		}},
	}
	for range 10 {
		client.responses = append(client.responses, model.Response{
			ID: uuid.NewString(), StopReason: model.StopReasonMaxTokens,
		})
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "Produce useful work.", fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		ModelRetryDelay: func(time.Duration) time.Duration { return 0 }}
	for i := range 11 {
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		if i < 10 {
			work = continueTurnOnNewLeaseForKernelTest(
				t, ctx, fixture, work, fixture.Now.Add(time.Duration(i+1)*time.Second),
			)
		}
	}
	require.Equal(t, 11, client.respondedCount())
	require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
	var successfulCutoffs, errorOutputs int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE stop_reason='max_tokens'),
		count(*) FILTER (WHERE stop_reason='error') FROM model_outputs WHERE agent_id=$1`, agentID).Scan(
		&successfulCutoffs, &errorOutputs,
	))
	require.Zero(t, successfulCutoffs, "empty output must not create new frontiers and reset retries")
	require.Equal(t, 1, errorOutputs)
}

func TestAgentExecutorPayloadOverflowDoesNotReduceOutputAllowance(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(32_768)})
	client := &sequenceKernelModel{
		providerModelSlug: "output-recovery", preparedInputTokenEstimate: 500,
		capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(32_768)},
		errs: []error{model.ProviderError{
			Kind: model.ErrorKindPayloadTooLarge, Source: "test", Code: "payload_size", Message: "Request body too large",
		}},
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "A request with no old source.", fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
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

func TestAgentExecutorGenuinelyOversizedClosedMessageRecoversWithMarkedExcerpt(t *testing.T) {
	ctx := context.Background()
	original := "OLD_OVERSIZED_MESSAGE " +
		strings.Repeat("This old message must remain stored intact. ", 2_000) + " ORIGINAL_TAIL"
	fixture, agentID, userID := seedSoftContextHistoryWithText(t, ctx, original)
	client := oversizedClosedHistoryKernelModel{uncertainSoftContextClient()}
	const current = "CURRENT_USER_REQUEST_MUST_REMAIN_EXACT"
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, current, fixture.Now.Add(time.Second))
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	require.Contains(t, string(client.responded[0].ProviderRequest), original)
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Greater(t, client.respondedCount(), 3)
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(3*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	final := client.responded[len(client.responded)-1]
	require.NotNil(t, final.Bundle.ContextCheckpoint)
	require.Contains(t, string(final.ProviderRequest), current)
	require.NotContains(t, string(final.ProviderRequest), original)
	require.Contains(t, final.Bundle.ContextCheckpoint.Summary, "omitted")
	var sawExcerpt bool
	for _, sent := range client.responded {
		if isCompactionRequestBundle(sent.Bundle) {
			require.NotContains(t, string(sent.ProviderRequest), current)
			if strings.Contains(string(sent.ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT") {
				sawExcerpt = true
				require.Contains(t, string(sent.ProviderRequest), "ORIGINAL_TAIL")
			}
		}
	}
	require.True(t, sawExcerpt)
	var persistedExcerpts, originalBlocks int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts
		WHERE agent_id=$1 AND source_excerpt_bytes IS NOT NULL`, agentID).Scan(&persistedExcerpts))
	require.Positive(t, persistedExcerpts)
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM content_blocks
		WHERE agent_id=$1 AND text_content=$2`, agentID, original).Scan(&originalBlocks))
	require.Equal(t, 1, originalBlocks)
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}

func TestAgentExecutorActualOverflowReducesOutputAndCarriesAcrossMaxTokens(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/output-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(32_768)})
	client := &sequenceKernelModel{
		providerModelSlug: "output-recovery", preparedInputTokenEstimate: 500,
		capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(32_768)},
		errs: []error{model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length",
			Message: "Input plus output exceeds provider capacity",
		}},
		responses: []model.Response{
			{ID: "partial-recovered-output", StopReason: model.StopReasonMaxTokens,
				Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Useful partial work."}}},
			completeProgressiveSummaryResponse("Completed the work."),
		},
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "Complete this work.", fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 1, client.respondedCount())
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 2, client.respondedCount())
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Equal(t, 3, client.respondedCount())
	require.Equal(t, 32_768, client.responded[0].Policy.MaxOutputTokens)
	require.Equal(t, 16_384, client.responded[1].Policy.MaxOutputTokens)
	require.Equal(t, 16_384, client.responded[2].Policy.MaxOutputTokens,
		"max_tokens continuation must not reset the rejected output allowance")
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}

func assertNoTerminalContextErrors(t *testing.T, ctx context.Context, fixture kernelFixture, agentID uuid.UUID) {
	t.Helper()
	var errors int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM model_outputs WHERE agent_id = $1 AND stop_reason = 'error'`, agentID,
	).Scan(&errors))
	require.Zero(t, errors)
}
