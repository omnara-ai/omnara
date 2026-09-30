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
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type checkpointExcerptKernelModel struct {
	*sequenceKernelModel
	overflowKind      model.ErrorKind
	refuseSummary     bool
	maxCheckpointSize int
	rejectNormal      bool
	unproductiveOnce  bool
	summaryCalls      int
}

func (m *checkpointExcerptKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	prepared := m.prepared[len(m.prepared)-1]
	if isCompactionRequestBundle(prepared.Bundle) {
		m.summaryCalls++
		response := completeProgressiveSummaryResponse(
			"REWRITTEN_HEAD " + strings.Repeat("preserve important prior decisions ", 80) + " REWRITTEN_TAIL",
		)
		response.ID = "checkpoint-rewrite"
		if m.refuseSummary {
			response = model.Response{ID: "refused-summary", StopReason: model.StopReasonRefusal}
		}
		m.responses = append([]model.Response{response}, m.responses...)
	} else if m.rejectNormal || (prepared.Bundle.ContextCheckpoint != nil &&
		len(prepared.Bundle.ContextCheckpoint.Summary) > m.maxCheckpointSize) {
		m.errs = append([]error{model.ProviderError{
			Kind: m.overflowKind, Source: "test", Code: "request_capacity", Message: "Request exceeds capacity",
		}}, m.errs...)
	} else {
		response := completeProgressiveSummaryResponse("The current request completed with the available history.")
		if m.unproductiveOnce {
			m.unproductiveOnce = false
			response = model.Response{ID: "no-progress", StopReason: model.StopReasonMaxTokens}
		}
		m.responses = append([]model.Response{response}, m.responses...)
	}
	return m.sequenceKernelModel.Respond(ctx, request)
}

func checkpointExcerptClient(kind model.ErrorKind) *checkpointExcerptKernelModel {
	return &checkpointExcerptKernelModel{
		sequenceKernelModel: &sequenceKernelModel{
			providerModelSlug: "soft-context", preparedInputTokenEstimate: 500,
			capabilities: model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1024)},
		},
		overflowKind: kind, maxCheckpointSize: 1_000,
	}
}

func TestAgentExecutorRecoversWithDurableCheckpointExcerptsAfterOneRewrite(t *testing.T) {
	for _, kind := range []model.ErrorKind{model.ErrorKindContextWindow, model.ErrorKindPayloadTooLarge} {
		for _, refuseSummary := range []bool{false, true} {
			name := string(kind) + "/completed_rewrite"
			if refuseSummary {
				name = string(kind) + "/failed_rewrite"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				original := "ORIGINAL_HEAD " + strings.Repeat("historical task state ", 2_000) + " ORIGINAL_TAIL"
				const current = "CURRENT_REQUEST_REMAINS_COMPLETE_🙂"
				fixture, work, prior := seedCheckpointBeforeCurrentAttempt(t, ctx, original, current)
				client := checkpointExcerptClient(kind)
				client.refuseSummary = refuseSummary
				var initialMessages []modelcontext.Message
				for attempt := range 60 {
					executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
						ModelRetryDelay: immediateKernelModelRetryDelay}
					require.NoError(t, executor.ExecuteModelWork(ctx, work))
					if attempt == 0 {
						initialMessages = client.responded[0].Bundle.Messages
					}
					if pendingModelWork(t, ctx, fixture, work.AgentID) == 0 {
						break
					}
					work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
						fixture.Now.Add(time.Duration(attempt+3)*time.Second))
				}
				require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
				require.Equal(t, 1, client.summaryCalls)
				assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
				final := client.responded[len(client.responded)-1]
				require.False(t, isCompactionRequestBundle(final.Bundle))
				require.Contains(t, final.Bundle.ContextCheckpoint.Summary, "Earlier history excerpted")
				if refuseSummary {
					require.Equal(t, prior.ID.String(), final.Bundle.ContextCheckpoint.ID)
				} else {
					require.NotEqual(t, prior.ID.String(), final.Bundle.ContextCheckpoint.ID)
				}
				var excerpts int
				var previousLength int
				for _, sent := range client.responded {
					if isCompactionRequestBundle(sent.Bundle) {
						require.NotContains(t, string(sent.ProviderRequest), current)
						continue
					}
					require.Equal(t, initialMessages, sent.Bundle.Messages)
					require.Contains(t, string(sent.ProviderRequest), current)
					if kind == model.ErrorKindPayloadTooLarge {
						require.Equal(t, 1024, sent.Policy.MaxOutputTokens)
					}
					if strings.Contains(sent.Bundle.ContextCheckpoint.Summary, "Earlier history excerpted") {
						if previousLength > 0 {
							require.Less(t, len(sent.ProviderRequest), previousLength)
						}
						previousLength = len(sent.ProviderRequest)
						excerpts++
						if kind == model.ErrorKindContextWindow {
							require.Equal(t, 1, sent.Policy.MaxOutputTokens)
						}
					}
				}
				require.Greater(t, excerpts, 1)
				stored, found, err := fixture.Store.Execution().GetContextCheckpoint(
					ctx, kernelTestProjectID, work.AgentID, prior.ID,
				)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, original, stored.Summary)
				rows, err := fixture.Pool.Query(ctx, `SELECT recovery_checkpoint_id,recovery_checkpoint_retained_bytes
					FROM model_call_contexts WHERE agent_id=$1 AND recovery_checkpoint_retained_bytes IS NOT NULL
					ORDER BY created_at`, work.AgentID)
				require.NoError(t, err)
				var savedID uuid.UUID
				previousBytes, savedCount := len(original), 0
				for rows.Next() {
					var id uuid.UUID
					var retained int
					require.NoError(t, rows.Scan(&id, &retained))
					if savedID != uuid.Nil {
						require.Equal(t, savedID, id)
					}
					require.Less(t, retained, previousBytes)
					savedID, previousBytes = id, retained
					savedCount++
				}
				require.NoError(t, rows.Err())
				rows.Close()
				require.Equal(t, excerpts, savedCount)
			})
		}
	}
}

func TestAgentExecutorCheckpointMarkerOnlyRecoveryStopsAndLaterTurnRemainsUsable(t *testing.T) {
	ctx := context.Background()
	original := strings.Repeat("saved earlier history ", 2_000)
	const current = "CURRENT_REQUEST_TOO_LARGE_EVEN_WITHOUT_HISTORY"
	fixture, work, prior := seedCheckpointBeforeCurrentAttempt(t, ctx, original, current)
	client := checkpointExcerptClient(model.ErrorKindPayloadTooLarge)
	client.refuseSummary, client.rejectNormal = true, true
	for attempt := range 30 {
		executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		if pendingModelWork(t, ctx, fixture, work.AgentID) == 0 {
			break
		}
		work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
			fixture.Now.Add(time.Duration(attempt+3)*time.Second))
	}
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	require.Less(t, len(client.responded), 20)
	require.Equal(t, 1, client.summaryCalls)
	final := client.responded[len(client.responded)-1]
	require.Equal(t, "[Earlier history omitted to fit the model. The original checkpoint remains stored.]",
		final.Bundle.ContextCheckpoint.Summary)
	require.Contains(t, string(final.ProviderRequest), current)
	var markerAttempts, terminalErrors int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER(WHERE recovery_checkpoint_retained_bytes=0),
		count(*) FILTER(WHERE error_code='context_cannot_be_compacted')
		FROM model_call_contexts WHERE agent_id=$1`, work.AgentID).Scan(&markerAttempts, &terminalErrors))
	require.Equal(t, 1, markerAttempts)
	require.Equal(t, 1, terminalErrors)
	stored, found, err := fixture.Store.Execution().GetContextCheckpoint(ctx, kernelTestProjectID, work.AgentID, prior.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, original, stored.Summary)
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitContentInputTurn(t, ctx, work.AgentID, kernelTestUserID,
		"A later small request.", fixture.Now.Add(time.Hour))
	client.rejectNormal, client.maxCheckpointSize = false, 1_000
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	require.Contains(t, string(client.responded[len(client.responded)-1].ProviderRequest), "A later small request.")
	require.Equal(t, 1, client.summaryCalls)
}

func TestAgentExecutorCheckpointExcerptSurvivesTransientEmptyMaxTokens(t *testing.T) {
	ctx := context.Background()
	fixture, work, _ := seedCheckpointBeforeCurrentAttempt(t, ctx,
		strings.Repeat("history ", 3_000), "Continue the current request.")
	client := checkpointExcerptClient(model.ErrorKindContextWindow)
	client.refuseSummary, client.unproductiveOnce = true, true
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
	require.False(t, client.unproductiveOnce)
	last := client.responded[len(client.responded)-1]
	previous := client.responded[len(client.responded)-2]
	require.Equal(t, previous.Bundle.ContextCheckpoint, last.Bundle.ContextCheckpoint)
	require.Equal(t, 1, previous.Policy.MaxOutputTokens)
	require.Equal(t, 1024, last.Policy.MaxOutputTokens)
	require.Equal(t, 1, client.summaryCalls)
	assertNoTerminalContextErrors(t, ctx, fixture, work.AgentID)
}

func TestAgentExecutorNeverExcerptsCheckpointCoveringCurrentOpening(t *testing.T) {
	ctx := context.Background()
	fixture, work, _ := seedCheckpointBeforeCurrentAttempt(t, ctx,
		strings.Repeat("earlier history ", 1_000), "Keep the current request's instructions.")
	partial := completeProgressiveSummaryResponse("Useful work completed; more remains.")
	partial.StopReason = model.StopReasonMaxTokens
	seedClient := &sequenceKernelModel{
		providerModelSlug: "soft-context", preparedInputTokenEstimate: 500,
		capabilities: model.Capabilities{ContextWindowTokens: 10_000, MaxOutputTokens: new(1024)},
		responses:    []model.Response{partial},
	}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, seedClient)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(3*time.Second))
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForModelContext(ctx, kernelTestProjectID, work.AgentID)
	require.NoError(t, err)
	parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: kernelTestProjectID, AgentID: work.AgentID, RuntimeLockID: work.RuntimeLockID,
		OpeningInputIDs: work.InputIDs, AgentConfigID: snapshot.AgentConfig.ID,
		InputEventSequence:       snapshot.InputEventSequence,
		SourceModelCallContextID: work.SourceModelCallContextID, SourceModelOutputID: work.SourceModelOutputID,
	})
	require.NoError(t, err)
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID, SourceEventSequenceEnd: snapshot.InputEventSequence,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: kernelTestProjectID, AgentID: work.AgentID, RuntimeLockID: work.RuntimeLockID,
				ModelCallContextID: parent.Context.ID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
				ErrorKind: model.ErrorKindContextWindow, ErrorCode: "context_length", ErrorMessage: "Input too large",
			},
		})
	require.NoError(t, err)
	const summary = "CURRENT_REQUEST_STATE_MUST_NOT_BE_EXCERPTED"
	checkpoint, err := fixture.Store.Execution().PublishContextCheckpoint(ctx,
		executionstore.PublishContextCheckpointInput{
			ProjectID: kernelTestProjectID, AgentID: work.AgentID, RuntimeLockID: work.RuntimeLockID,
			ModelCallContextID: handoff.CompactionCall.Context.ID, Summary: strings.Repeat(summary, 100),
			APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		})
	require.NoError(t, err)
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(4*time.Second))
	require.GreaterOrEqual(t, checkpoint.SummarizedThroughEventSequence, work.OpeningEventSequence)
	client := checkpointExcerptClient(model.ErrorKindPayloadTooLarge)
	client.refuseSummary, client.rejectNormal = true, true
	executor = AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(5*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Zero(t, pendingModelWork(t, ctx, fixture, work.AgentID))
	require.Equal(t, 1, client.summaryCalls)
	for _, sent := range client.responded {
		if !isCompactionRequestBundle(sent.Bundle) {
			require.Equal(t, checkpoint.Summary, sent.Bundle.ContextCheckpoint.Summary)
		}
	}
	var excerptCount int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts
		WHERE agent_id=$1 AND recovery_checkpoint_retained_bytes IS NOT NULL`, work.AgentID).Scan(&excerptCount))
	require.Zero(t, excerptCount)
}
