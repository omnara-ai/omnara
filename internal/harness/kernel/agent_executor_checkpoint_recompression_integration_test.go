//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func seedCheckpointBeforeCurrentAttempt(
	t *testing.T, ctx context.Context, summary, current string,
) (kernelFixture, ModelWorkExecution, executionstore.ContextCheckpointRecord) {
	t.Helper()
	fixture, agentID, userID := seedSoftContextHistoryWithText(t, ctx, strings.Repeat("original closed history ", 4000))
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, current, fixture.Now.Add(time.Second))
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForModelContext(ctx, kernelTestProjectID, agentID)
	require.NoError(t, err)
	parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: kernelTestProjectID, AgentID: agentID, RuntimeLockID: work.RuntimeLockID,
		OpeningInputIDs: work.InputIDs, AgentConfigID: snapshot.AgentConfig.ID,
		InputEventSequence: snapshot.InputEventSequence,
	})
	require.NoError(t, err)
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID, SourceEventSequenceEnd: work.OpeningEventSequence - 1,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: kernelTestProjectID, AgentID: agentID, RuntimeLockID: work.RuntimeLockID,
				ModelCallContextID: parent.Context.ID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
				ErrorKind: model.ErrorKindContextWindow, ErrorCode: "context_length", ErrorMessage: "Input too large",
			},
		})
	require.NoError(t, err)
	checkpoint, err := fixture.Store.Execution().PublishContextCheckpoint(ctx,
		executionstore.PublishContextCheckpointInput{
			ProjectID: kernelTestProjectID, AgentID: agentID, RuntimeLockID: work.RuntimeLockID,
			ModelCallContextID: handoff.CompactionCall.Context.ID, Summary: summary,
			APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		})
	require.NoError(t, err)
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	return fixture, work, checkpoint
}

type checkpointRecompressionKernelModel struct {
	*sequenceKernelModel
	originalSummary string
	outputOnly      bool
}

func (m checkpointRecompressionKernelModel) Respond(
	ctx context.Context, request model.Request,
) (model.Response, error) {
	prepared := m.prepared[len(m.prepared)-1]
	isSummary := isCompactionRequestBundle(prepared.Bundle)
	reject := isSummary && len(request.ProviderRequest) > 12_000
	if !isSummary {
		if m.outputOnly {
			reject = prepared.Policy.MaxOutputTokens > 512
		} else {
			reject = prepared.Bundle.ContextCheckpoint != nil &&
				prepared.Bundle.ContextCheckpoint.Summary == m.originalSummary
		}
	}
	if reject {
		m.errs = append([]error{model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Code: "context_length", Message: "Request exceeds capacity",
		}}, m.errs...)
	} else {
		m.responses = append([]model.Response{
			completeProgressiveSummaryResponse("Preserve the established objective and continue the current request."),
		}, m.responses...)
	}
	return m.sequenceKernelModel.Respond(ctx, request)
}

func TestAgentExecutorRecompressesOversizedCheckpointWithoutConsumingCurrentRequest(t *testing.T) {
	ctx := context.Background()
	original := "OLD_CHECKPOINT_START " + strings.Repeat("important earlier state ", 2000) + " OLD_CHECKPOINT_END"
	const current = "CURRENT_REQUEST_MUST_REMAIN_EXACT"
	fixture, work, prior := seedCheckpointBeforeCurrentAttempt(t, ctx, original, current)
	client := checkpointRecompressionKernelModel{
		sequenceKernelModel: &sequenceKernelModel{
			providerModelSlug: "soft-context", preparedInputTokenEstimate: 500,
			capabilities: model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1024)},
		},
		originalSummary: original,
	}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	for attempt := range 20 {
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		if pendingModelWork(t, ctx, fixture, work.AgentID) == 0 {
			break
		}
		work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
			fixture.Now.Add(time.Duration(attempt+3)*time.Second))
	}
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
	final := client.responded[len(client.responded)-1]
	require.False(t, isCompactionRequestBundle(final.Bundle))
	require.Contains(t, string(final.ProviderRequest), current)
	require.NotNil(t, final.Bundle.ContextCheckpoint)
	require.Equal(t, prior.SummarizedThroughEventSequence, final.Bundle.ContextCheckpoint.SummarizedThroughEventSequence)
	require.Contains(t, final.Bundle.ContextCheckpoint.Summary, "omitted")
	require.Equal(t, 1024, final.Policy.MaxOutputTokens, "recompressed input must not inherit the old one-token cap")
	var fullSummaryAttempt, excerptAttempt bool
	for _, sent := range client.responded {
		if isCompactionRequestBundle(sent.Bundle) {
			require.NotContains(t, string(sent.ProviderRequest), current)
			fullSummaryAttempt = fullSummaryAttempt || strings.Contains(string(sent.ProviderRequest), original)
			excerptAttempt = excerptAttempt || strings.Contains(string(sent.ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT")
		}
	}
	require.True(t, fullSummaryAttempt)
	require.True(t, excerptAttempt)
	stored, found, err := fixture.Store.Execution().GetContextCheckpoint(ctx, kernelTestProjectID, work.AgentID, prior.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, original, stored.Summary)
	var replacementCount int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts
		WHERE agent_id=$1 AND replaces_checkpoint_id=$2 AND state='succeeded'`,
		work.AgentID, prior.ID).Scan(&replacementCount))
	require.Equal(t, 1, replacementCount)
}

func TestAgentExecutorReducesOutputBeforeRecompressingSmallCheckpoint(t *testing.T) {
	ctx := context.Background()
	fixture, work, prior := seedCheckpointBeforeCurrentAttempt(t, ctx, "A small existing checkpoint.", "Continue.")
	client := checkpointRecompressionKernelModel{
		sequenceKernelModel: &sequenceKernelModel{
			providerModelSlug: "soft-context", preparedInputTokenEstimate: 500,
			capabilities: model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1024)},
		},
		outputOnly: true,
	}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(3*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Len(t, client.responded, 2)
	for _, sent := range client.responded {
		require.False(t, isCompactionRequestBundle(sent.Bundle))
		require.Equal(t, prior.Summary, sent.Bundle.ContextCheckpoint.Summary)
	}
	require.Equal(t, 512, client.responded[1].Policy.MaxOutputTokens)
	assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
}
