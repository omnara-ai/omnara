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
}

func (m checkpointRecompressionKernelModel) Respond(
	ctx context.Context, request model.Request,
) (model.Response, error) {
	prepared := m.prepared[len(m.prepared)-1]
	isSummary := isCompactionRequestBundle(prepared.Bundle)
	reject := isSummary && len(request.ProviderRequest) > 12_000
	if !isSummary {
		reject = prepared.Bundle.ContextCheckpoint != nil &&
			prepared.Bundle.ContextCheckpoint.Summary == m.originalSummary
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
