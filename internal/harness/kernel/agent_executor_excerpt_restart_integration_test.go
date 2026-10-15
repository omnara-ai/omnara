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

func TestAgentExecutorReconstructsExcerptAfterTransientFailureOnNewLease(t *testing.T) {
	ctx := context.Background()
	const current = "CURRENT_REQUEST_RETAINED_DURING_RESTART"
	original := "OLD_HEAD " + strings.Repeat("closed history remains stored ", 2_000) + " OLD_TAIL"
	fixture, agentID, userID := seedSoftContextHistoryWithText(t, ctx, original)
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, current, fixture.Now.Add(time.Second))
	base := oversizedClosedHistoryKernelModel{uncertainSoftContextClient()}
	client := &transientExcerptKernelModel{sequenceKernelModel: base.sequenceKernelModel, respond: base.Respond}
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
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT id FROM model_call_contexts
		WHERE agent_id=$1 AND operation_kind='compaction' AND recovery_kind='retry'
		ORDER BY created_at DESC LIMIT 1`, work.AgentID).Scan(&failedContextID))
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
	require.NotContains(t, string(final.ProviderRequest), original)
	require.Contains(t, final.Bundle.ContextCheckpoint.Summary, "omitted")
	var fullSummaryAttempt, excerptAttempt bool
	for _, sent := range client.responded {
		if !isCompactionRequestBundle(sent.Bundle) {
			require.Contains(t, string(sent.ProviderRequest), current)
			continue
		}
		require.NotContains(t, string(sent.ProviderRequest), current)
		if strings.Contains(string(sent.ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT") {
			require.True(t, fullSummaryAttempt, "the full source must reach the provider before excerpting")
			require.Contains(t, string(sent.ProviderRequest), "OLD_HEAD")
			require.Contains(t, string(sent.ProviderRequest), "OLD_TAIL")
			excerptAttempt = true
		}
		fullSummaryAttempt = fullSummaryAttempt || strings.Contains(string(sent.ProviderRequest), original)
	}
	require.True(t, excerptAttempt)
	var originalBlocks int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM content_blocks
		WHERE agent_id=$1 AND text_content=$2`, work.AgentID, original).Scan(&originalBlocks))
	require.Equal(t, 1, originalBlocks)
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
}
