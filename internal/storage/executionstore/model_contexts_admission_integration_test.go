//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestNormalCompletionExposesContextUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		toolResponse bool
		usage        modelenvelope.Usage
	}{
		{"text_with_usage", false, modelenvelope.Usage{InputTokens: 120, UncachedInputTokens: 120, OutputTokens: 30}},
		{"tool_with_usage", true, modelenvelope.Usage{InputTokens: 120, UncachedInputTokens: 120, OutputTokens: 30}},
		{"tool_without_usage", true, modelenvelope.Usage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "context_usage_"+tc.name)
			slug := modelProviderSlugForContext(
				t,
				ctx,
				fixture.Store,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			)

			envelope := modelenvelope.ResponseEnvelope{
				InputFingerprint: strings.Repeat("a", 64), RequestedProviderModelSlug: slug,
				ServedProviderModelSlug: slug,
				APIFormat:               modelprotocol.APIFormatOpenAIResponses,
				APIVariant:              modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "usage-response",
					Usage:      tc.usage,
					StopReason: modelenvelope.StopReasonEndTurn,
					Content: []modelenvelope.ResponsePart{
						{
							Type: modelenvelope.ResponsePartTypeText,
							Text: "done",
						},
					},
				},
			}
			if tc.toolResponse {
				envelope.Normalized.StopReason = modelenvelope.StopReasonToolUse
				envelope.Normalized.Content = []modelenvelope.ResponsePart{
					{
						Type:           modelenvelope.ResponsePartTypeToolCall,
						ProviderCallID: "usage-call",
						ToolName:       "read_file",
						ToolInput: json.RawMessage(
							`{"path":"/artifacts/invalid"}`,
						),
					},
				}
			}
			complete := func() (uuid.UUID, []uuid.UUID, error) {
				if tc.toolResponse {
					event, calls, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
						ctx,
						executionstore.RecordToolCallSourceAndCompleteContextInput{
							ProjectID:          testProjectID,
							AgentID:            fixture.AgentID,
							RuntimeLockID:      fixture.Lock.ID,
							ModelCallContextID: claim.Context.ID,
							ProviderResponse:   envelope,
							ToolCallBindings: []executionstore.ToolCallBindingInput{
								{
									ProviderCallID: "usage-call",
									Type:           toolcatalog.ToolTypeBuiltIn,
								},
							},
						},
					)
					ids := make([]uuid.UUID, len(calls))
					for i, call := range calls {
						ids[i] = call.ID
					}
					return event.ID, ids, err
				}
				event, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(
					ctx,
					executionstore.RecordModelOutputAndCompleteContextInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: claim.Context.ID, ProviderResponse: envelope,
					},
				)
				return event.ID, nil, err
			}
			if !tc.toolResponse {
				envelope.InputFingerprint = "invalid"
				_, _, err := complete()
				assertPgConstraint(t, err, "23514", "model_call_contexts_input_fingerprint")
				envelope.InputFingerprint = strings.Repeat("a", 64)
			}
			eventID, toolIDs, err := complete()
			require.NoError(t, err)
			loadRecord := func() executionstore.ModelCallContextRecord {
				record, found, err := fixture.Store.Execution().GetModelCallContext(
					ctx,
					testProjectID,
					fixture.AgentID,
					claim.Context.ID,
				)
				require.NoError(t, err)
				require.True(t, found)
				return record
			}
			record := loadRecord()
			require.Equal(t, envelope.Normalized.Usage, record.Usage)
			if tc.usage.InputTokens > 0 {
				require.Equal(t, envelope.InputFingerprint, record.InputFingerprint)
			} else {
				require.Empty(t, record.InputFingerprint)
			}
			replayed := envelope
			replayed.InputFingerprint = strings.Repeat("b", 64)
			replayed.Normalized.Usage.InputTokens += 7
			replayed.Normalized.Usage.UncachedInputTokens += 7
			original := envelope
			envelope = replayed
			replayEventID, replayToolIDs, err := complete()
			envelope = original
			require.NoError(t, err)
			require.Equal(t, eventID, replayEventID)
			require.Equal(t, toolIDs, replayToolIDs)
			require.Equal(t, record, loadRecord())
			watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
			require.NoError(t, err)
			events, err := fixture.Store.Execution().ListContextEvents(ctx, testProjectID, fixture.AgentID, 0, watermark, 100)
			require.NoError(t, err)
			matched := false
			for _, event := range events {
				if event.ModelCallContextID != record.ID {
					continue
				}
				matched = true
				require.Equal(t, record.InputFingerprint, event.InputFingerprint)
				require.Equal(t, record.Usage, event.Usage)
				require.Equal(t, record.InputEventSequence, event.InputEventSequence)
				require.Equal(t, record.AgentConfigID, event.AgentConfigID)
				require.Equal(t, record.ConfiguredModelRevisionID, event.ConfiguredModelRevisionID)
			}
			require.True(t, matched)
		})
	}
}

func TestContextEventsExcludeFailedModelUsage(t *testing.T) {
	t.Parallel()
	for _, compaction := range []bool{false, true} {
		name := "normal"
		if compaction {
			name = "compaction"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "failed_usage_"+name)
			usage := modelenvelope.Usage{InputTokens: 120, UncachedInputTokens: 120, OutputTokens: 30}
			if compaction {
				claim = admissionHandoff(t, ctx, fixture, claim, claim.Context.InputEventSequence).CompactionCall
				err := fixture.Store.Execution().RecordTerminalCompactionFailure(ctx,
					executionstore.RecordTerminalCompactionFailureInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: claim.Context.ID, Usage: usage,
						APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
						ErrorKind: modelprotocol.ErrorKindInvalidRequest, ErrorMessage: "summary failed",
					})
				require.NoError(t, err)
			} else {
				_, err := fixture.Store.Execution().RecordModelCallErrorAndCompleteContext(ctx,
					executionstore.RecordModelCallErrorAndCompleteContextInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: claim.Context.ID, Usage: usage,
						APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
						ErrorKind: modelprotocol.ErrorKindInvalidRequest, ErrorMessage: "normal request failed",
					})
				require.NoError(t, err)
			}
			record, found, err := fixture.Store.Execution().GetModelCallContext(
				ctx, testProjectID, fixture.AgentID, claim.Context.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, usage, record.Usage)
			watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
			require.NoError(t, err)
			events, err := fixture.Store.Execution().ListContextEvents(ctx, testProjectID, fixture.AgentID, 0, watermark, 100)
			require.NoError(t, err)
			matched := false
			for _, event := range events {
				if event.ModelCallContextID == claim.Context.ID {
					matched = true
					require.Equal(t, modelenvelope.Usage{}, event.Usage)
				}
			}
			require.True(t, matched)
		})
	}
}

func admissionHandoff(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	parent executionstore.ModelCallClaim,
	sourceEnd int64,
) executionstore.TriggeredCompactionHandoff {
	t.Helper()
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID, SourceEventSequenceEnd: sourceEnd,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: parent.Context.ID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
				ErrorKind: modelprotocol.ErrorKindContextWindow,
				ErrorCode: "test_overflow", ErrorMessage: "test overflow",
			},
		},
	)
	require.NoError(t, err)
	require.True(t, handoff.CompactionCall.Claimed)
	return handoff
}

func TestCompactionExcerptProgressSharesTransientRetryBudget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "excerpt_retry_budget")
	claim := admissionHandoff(t, ctx, fixture, parent, parent.Context.InputEventSequence).CompactionCall
	for range executionpolicy.MaxModelCallRetriesPerOperation {
		_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
			ctx,
			executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				RuntimeLockID:      fixture.Lock.ID,
				ModelCallContextID: claim.Context.ID,
				ErrorKind:          modelprotocol.ErrorKindProviderUnavailable,
				ErrorMessage:       "temporary failure",
			},
		)
		require.NoError(t, err)
		claim, err = fixture.Store.Execution().ClaimNextModelCallContext(
			ctx,
			executionstore.ClaimNextModelCallContextInput{
				ProjectID:                     testProjectID,
				AgentID:                       fixture.AgentID,
				RuntimeLockID:                 fixture.Lock.ID,
				PredecessorModelCallContextID: claim.Context.ID,
			},
		)
		require.NoError(t, err)
		require.True(t, claim.Claimed)
	}
	replacement, err := fixture.Store.Execution().ReplaceCompactionSource(
		ctx,
		executionstore.ReplaceCompactionSourceInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindContextWindow,
			ErrorMessage:       "source too large",
			APIFormat:          modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
			NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd,
		},
	)
	require.NoError(t, err)
	require.True(t, replacement.CompactionCall.Claimed)
	require.Equal(t, 1, replacement.CompactionCall.Context.SourceExcerptReductionCount)
	require.Equal(t, claim.Context.AttemptNumber+1, replacement.CompactionCall.Context.AttemptNumber)
	replay, err := fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindContextWindow,
		ErrorMessage: "source too large", APIFormat: modelprotocol.APIFormatOpenAIResponses,
		APIVariant: modelprotocol.APIVariantDefault, NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd,
	})
	require.NoError(t, err)
	require.False(t, replay.CompactionCall.Created)
	require.Equal(t, replacement.CompactionCall.Context.ID, replay.CompactionCall.Context.ID)

	stale, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: claim.Context.ID,
	})
	require.NoError(t, err)
	require.False(t, stale.Created)
	require.False(t, stale.Claimed)
	require.Equal(t, replacement.CompactionCall.Context.ID, stale.Context.ID)
	snapshot, err := fixture.Store.Execution().IntegrationLoadExecution(ctx, testProjectID, fixture.AgentID)
	require.NoError(t, err)
	require.True(t, snapshot.Selection.TurnContinuable)
	require.Equal(t, replacement.CompactionCall.Context.ID, snapshot.Head.CompactionContextID)
	retryCount, err := fixture.Store.Execution().CountModelCallRetries(
		ctx,
		testProjectID,
		fixture.AgentID,
		replacement.CompactionCall.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, executionpolicy.MaxModelCallRetriesPerOperation, retryCount)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: replacement.CompactionCall.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindProviderUnavailable,
			ErrorMessage:       "another failure",
		},
	)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID:                  testProjectID,
		AgentID:                    fixture.AgentID,
		RuntimeLockID:              fixture.Lock.ID,
		ModelCallContextID:         replacement.CompactionCall.Context.ID,
		ErrorKind:                  modelprotocol.ErrorKindContextWindow,
		ErrorMessage:               "missing provider attempt evidence",
		NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd,
	})
	require.True(t, errors.Is(err, storeerr.ErrStateTransitionConflict))
}

func TestLegacyScheduledCompactionRetryCanRunAfterSharedBudgetIsExhausted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "legacy_compaction_retry")
	claim := admissionHandoff(t, ctx, fixture, parent, parent.Context.InputEventSequence).CompactionCall
	for retry := 0; retry <= executionpolicy.MaxModelCallRetriesPerOperation; retry++ {
		if retry == executionpolicy.MaxModelCallRetriesPerOperation {
			replacement, err := fixture.Store.Execution().ReplaceCompactionSource(ctx,
				executionstore.ReplaceCompactionSourceInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindContextWindow,
					ErrorMessage:               "source too large",
					NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd - 1,
				})
			require.NoError(t, err)
			require.True(t, replacement.CompactionCall.Claimed)
			require.Equal(t, 1, replacement.CompactionCall.Context.AttemptNumber)
			stale, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx,
				executionstore.ClaimNextModelCallContextInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					PredecessorModelCallContextID: claim.Context.ID,
				})
			require.NoError(t, err)
			require.False(t, stale.Created)
			require.False(t, stale.Claimed)
			require.Equal(t, replacement.CompactionCall.Context.ID, stale.Context.ID)
			claim = replacement.CompactionCall
		}
		_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed', recovery_kind='retry', retry_at=statement_timestamp(),
    error_kind='provider_unavailable', error_message='legacy retry scheduled per source range',
    completed_at=statement_timestamp() WHERE id=$1`, claim.Context.ID)
		require.NoError(t, err)
		require.NoError(t, fixture.Store.Execution().RepairExecutionForTest(ctx, testProjectID, fixture.AgentID))
		claim, err = fixture.Store.Execution().ClaimNextModelCallContext(ctx,
			executionstore.ClaimNextModelCallContextInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				PredecessorModelCallContextID: claim.Context.ID,
			})
		require.NoError(t, err)
		require.True(t, claim.Claimed)
	}
	retryCount, err := fixture.Store.Execution().CountModelCallRetries(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, executionpolicy.MaxModelCallRetriesPerOperation+1, retryCount)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
	))
	finished, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelCallContextFailed, finished.State)
	require.Empty(t, finished.RecoveryKind)
	_, found, err = fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
	require.NoError(t, err)
	require.False(t, found)
}
