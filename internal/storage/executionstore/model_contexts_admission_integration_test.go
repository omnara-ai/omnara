//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
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
			complete := func() error {
				if tc.toolResponse {
					_, _, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
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
					return err
				}
				_, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(
					ctx,
					executionstore.RecordModelOutputAndCompleteContextInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: claim.Context.ID, ProviderResponse: envelope,
					},
				)
				return err
			}
			if !tc.toolResponse {
				envelope.InputFingerprint = "invalid"
				assertPgConstraint(t, complete(), "23514", "model_call_contexts_input_fingerprint")
				envelope.InputFingerprint = strings.Repeat("a", 64)
			}
			require.NoError(t, complete())
			require.NoError(t, complete())
			record, found, err := fixture.Store.Execution().GetModelCallContext(
				ctx,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, envelope.Normalized.Usage, record.Usage)
			if tc.usage.InputTokens > 0 {
				require.Equal(t, envelope.InputFingerprint, record.InputFingerprint)
				envelope.InputFingerprint = strings.Repeat("b", 64)
				require.ErrorIs(t, complete(), storeerr.ErrIdempotencyConflict)
			} else {
				require.Empty(t, record.InputFingerprint)
			}
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
				claim = admissionHandoff(t, ctx, fixture, claim, false, claim.Context.InputEventSequence).CompactionCall
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
	optional bool,
	sourceEnd int64,
) executionstore.TriggeredCompactionHandoff {
	t.Helper()
	kind := executionstore.ModelCallRecoveryCompact
	if optional {
		kind = executionstore.ModelCallRecoveryCompactOptional
	}
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID, SourceEventSequenceEnd: sourceEnd,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: parent.Context.ID, RecoveryKind: kind,
				ErrorKind: modelprotocol.ErrorKindContextWindow,
				ErrorCode: "test_overflow", ErrorMessage: "test overflow",
			},
		},
	)
	require.NoError(t, err)
	require.True(t, handoff.CompactionCall.Claimed)
	require.Equal(t, parent.Context.ID, handoff.CompactionCall.Context.ParentNormalModelCallContextID)
	return handoff
}

func TestOptionalCompactionFailureResumesNormalAndOwnsLaterRepair(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_repair_owner")
	sourceEnd := parent.Context.InputEventSequence
	optional := admissionHandoff(t, ctx, fixture, parent, true, sourceEnd)
	failure := executionstore.RecordOptionalCompactionFailureInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: optional.CompactionCall.Context.ID,
		APIFormat:          modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		ErrorKind: modelprotocol.ErrorKindTransient, ErrorCode: "summary_not_reduced",
		ErrorMessage: "optional summary ineffective",
		Usage:        modelenvelope.Usage{InputTokens: 200, UncachedInputTokens: 200, OutputTokens: 10},
	}
	err := fixture.Store.Execution().RecordTerminalCompactionFailure(ctx,
		executionstore.RecordTerminalCompactionFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: optional.CompactionCall.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindTransient,
			ErrorMessage:       "optional failure must not produce a terminal error output",
		})
	assertPgErrorMessage(t, err, "23514", "model call context outcome does not match its terminal state")
	failed, err := fixture.Store.Execution().RecordOptionalCompactionFailure(ctx, failure)
	require.NoError(t, err)
	require.Empty(t, failed.RecoveryKind)
	_, err = fixture.Store.Execution().RecordOptionalCompactionFailure(ctx, failure)
	require.NoError(t, err)
	changed := failure
	changed.ErrorMessage = "different evidence"
	_, err = fixture.Store.Execution().RecordOptionalCompactionFailure(ctx, changed)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	var workKind string
	var workID uuid.UUID
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT work_kind, model_call_context_id FROM agent_next_model_work($1,$2)`,
			testProjectID,
			fixture.AgentID,
		).Scan(
			&workKind,
			&workID,
		),
	)
	require.Equal(t, "resume", workKind)
	require.Equal(t, parent.Context.ID, workID)
	next, err := fixture.Store.Execution().ClaimNextModelCallContext(
		ctx,
		executionstore.ClaimNextModelCallContextInput{
			ProjectID:                     testProjectID,
			AgentID:                       fixture.AgentID,
			RuntimeLockID:                 fixture.Lock.ID,
			PredecessorModelCallContextID: workID,
		},
	)
	require.NoError(t, err)
	require.True(t, next.Claimed)
	require.Equal(t, 2, next.Context.AttemptNumber)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx,
		testProjectID,
		fixture.AgentID,
		next.Context.ID,
	)
	require.NoError(t, err)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
	require.True(t, state.HasPriorOptionalCompaction)
	retry, err := fixture.Store.Execution().GetModelCallRetryState(ctx, testProjectID, fixture.AgentID, next.Context.ID)
	require.NoError(t, err)
	require.Zero(t, retry.RetryCount)
	require.False(t, state.HasPriorNormalAttempt)
	var continuable int
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT count(*) FROM agent_continuable_model_contexts($1,$2) WHERE model_call_context_id=$3`,
			testProjectID,
			fixture.AgentID,
			failed.ID,
		).Scan(
			&continuable,
		),
	)
	require.Zero(t, continuable)
	required := admissionHandoff(t, ctx, fixture, next, false, sourceEnd)
	require.NotEqual(t, optional.CompactionCall.Context.ID, required.CompactionCall.Context.ID)
	var outputs int
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT count(*) FROM model_outputs WHERE agent_id=$1`,
			fixture.AgentID,
		).Scan(
			&outputs,
		),
	)
	require.Zero(t, outputs)
	checkpoint, err := publishCheckpointForRangeTest(
		t, ctx, fixture, required.CompactionCall, "required recovery after optional failure", fixture.Now,
	)
	require.NoError(t, err)
	require.Equal(t, required.CompactionCall.Context.ID, checkpoint.ProducerModelCallContextID)
}

func TestInterruptedOptionalCompactionResumesNormalAfterRuntimeRelease(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_interrupted")
	handoff := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	require.NoError(
		t,
		fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			testProjectID,
			fixture.AgentID,
			fixture.Lock.ID,
		),
	)
	child, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		handoff.CompactionCall.Context.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, child.RecoveryKind)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, child.ID,
	)
	require.NoError(t, err)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
	var workID uuid.UUID
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT model_call_context_id FROM agent_next_model_work($1,$2)`,
			testProjectID,
			fixture.AgentID,
		).Scan(
			&workID,
		),
	)
	require.Equal(t, parent.Context.ID, workID)
}

func TestCompactionExcerptProgressSharesTransientRetryBudget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "excerpt_retry_budget")
	claim := admissionHandoff(t, ctx, fixture, parent, false, parent.Context.InputEventSequence).CompactionCall
	_, err := fixture.Store.Execution().RecordOptionalCompactionFailure(
		ctx, executionstore.RecordOptionalCompactionFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindProviderUnavailable,
			ErrorMessage: "required compaction cannot skip recovery",
		},
	)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	for range executionstore.MaxModelCallRetriesPerOperation {
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
	stale, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: claim.Context.ID,
	})
	require.NoError(t, err)
	require.False(t, stale.Created)
	require.False(t, stale.Claimed)
	require.Equal(t, replacement.CompactionCall.Context.ID, stale.Context.ID)
	var continuableID uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		`SELECT model_call_context_id FROM agent_continuable_model_contexts($1,$2)`,
		testProjectID, fixture.AgentID).Scan(&continuableID))
	require.Equal(t, replacement.CompactionCall.Context.ID, continuableID)
	state, err := fixture.Store.Execution().GetModelCallRetryState(
		ctx,
		testProjectID,
		fixture.AgentID,
		replacement.CompactionCall.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.MaxModelCallRetriesPerOperation, state.RetryCount)
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

func TestOptionalCompactionObservationsResetAtAttemptAndModelRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_observation_scope")
	optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	_, err := fixture.Store.Execution().RecordOptionalCompactionFailure(ctx,
		executionstore.RecordOptionalCompactionFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: optional.CompactionCall.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindTransient, ErrorMessage: "optional refusal",
			APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
			Usage: modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10},
		})
	require.NoError(t, err)
	next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: parent.Context.ID,
	})
	require.NoError(t, err)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: next.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindProviderUnavailable, ErrorMessage: "failure with usage",
			APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
			Usage: modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10},
		})
	require.NoError(t, err)
	next, err = fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: next.Context.ID,
	})
	require.NoError(t, err)
	slug := modelProviderSlugForContext(t, ctx, fixture.Store, testProjectID, fixture.AgentID, next.Context.ID)
	_, err = fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: next.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: slug, ServedProviderModelSlug: slug,
				APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID: "observed-normal-response", StopReason: modelenvelope.StopReasonEndTurn,
					Usage:   modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 100},
					Content: []modelenvelope.ResponsePart{{Type: modelenvelope.ResponsePartTypeText, Text: "done"}},
				},
			},
		})
	require.NoError(t, err)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, next.Context.ID)
	require.NoError(t, err)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.Zero(t, state.LatestObservedNormalInputTokens)
	later := insertNormalContextAtLatestFrontier(t, fixture, next.Context.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, later.ID)
	require.NoError(t, err)
	require.Equal(t, 100, state.MinimumObservedNormalInputTokens)
	require.Equal(t, 100, state.LatestObservedNormalInputTokens)
	_, err = fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='compact_optional',
error_kind='context_window',error_message='later optional',completed_at=statement_timestamp() WHERE id=$1`, later.ID)
	require.NoError(t, err)
	afterOptional := insertNormalContextAtLatestFrontier(t, fixture, later.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, afterOptional.ID)
	require.NoError(t, err)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.True(t, state.HasPriorOptionalCompaction)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: afterOptional.ID,
			ErrorKind:          modelprotocol.ErrorKindRuntime, ErrorMessage: "retry with a new model revision",
		})
	require.NoError(t, err)
	original, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(
		ctx, testOrgID, parent.Context.ConfiguredModelRevisionID,
	)
	require.NoError(t, err)
	updated, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: testOrgID, ModelProviderConfigID: original.ModelProviderConfigID,
		ID: original.ConfiguredModelID, ContextWindowTokens: new(original.ContextWindowTokens + 1),
	})
	require.NoError(t, err)
	changed := insertNormalContextAtLatestFrontier(t, fixture, afterOptional.ID, updated.CurrentRevisionID, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, changed.ID)
	require.NoError(t, err)
	require.False(t, state.HasPriorOptionalCompaction)
	require.Zero(t, state.LatestObservedNormalInputTokens)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
}

func TestObservedCompactionPressureDoesNotResurrectUsageAfterUnknownSuccess(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "observed_pressure")
	optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	_, err := fixture.Store.Execution().RecordOptionalCompactionFailure(
		ctx, executionstore.RecordOptionalCompactionFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: optional.CompactionCall.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindTransient, ErrorCode: "summary_not_reduced",
			ErrorMessage: "temporary summary failure",
		},
	)
	require.NoError(t, err)
	claim, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: parent.Context.ID,
	})
	require.NoError(t, err)
	var pinnedContextID uuid.UUID
	for _, observation := range []struct{ input, minimum int }{{0, 0}, {2000, 2000}, {500, 500}, {0, 500}} {
		inputTokens := observation.input
		slug := modelProviderSlugForContext(t, ctx, fixture.Store, testProjectID, fixture.AgentID, claim.Context.ID)
		_, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(
			ctx, executionstore.RecordModelOutputAndCompleteContextInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: claim.Context.ID,
				ProviderResponse: modelenvelope.ResponseEnvelope{
					RequestedProviderModelSlug: slug, ServedProviderModelSlug: slug,
					APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
					Normalized: modelenvelope.ResponseNormalized{
						ID: claim.Context.ID.String(), StopReason: modelenvelope.StopReasonEndTurn,
						Usage:   modelenvelope.Usage{InputTokens: inputTokens, UncachedInputTokens: inputTokens},
						Content: []modelenvelope.ResponsePart{{Type: modelenvelope.ResponsePartTypeText, Text: "done"}},
					},
				},
			},
		)
		require.NoError(t, err)
		current := insertNormalContextAtLatestFrontier(t, fixture, claim.Context.ID, uuid.Nil, uuid.Nil)
		state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
		require.NoError(t, err)
		require.Equal(t, inputTokens, state.LatestObservedNormalInputTokens)
		require.Equal(t, observation.minimum, state.MinimumObservedNormalInputTokens)
		if inputTokens == 2000 {
			pinnedContextID = current.ID
		}
		require.True(t, state.HasPriorOptionalCompaction)
		claim.Context = current
	}
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, pinnedContextID,
	)
	require.NoError(t, err)
	require.Equal(t, 2000, state.MinimumObservedNormalInputTokens)
	require.Equal(t, 2000, state.LatestObservedNormalInputTokens)
	config := mustCreateAgentConfigFromYAML(t, ctx, fixture.Store,
		strings.Replace(testAgentConfigYAML(), "instruction: test", "instruction: changed headroom scope", 1))
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindRuntime,
			ErrorMessage: "retry with a new agent configuration",
		})
	require.NoError(t, err)
	changed := insertNormalContextAtLatestFrontier(t, fixture, claim.Context.ID, uuid.Nil, config.ID)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, changed.ID)
	require.NoError(t, err)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.Zero(t, state.LatestObservedNormalInputTokens)
}

func insertNormalContextAtLatestFrontier(
	t *testing.T,
	fixture processDaemonFixture,
	source, revision, config uuid.UUID,
) executionstore.ModelCallContextRecord {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
SELECT source.org_id,source.project_id,source.agent_id,'normal',
coalesce((SELECT max(prior.attempt_number)+1 FROM model_call_contexts prior
WHERE prior.agent_id=source.agent_id AND prior.operation_kind='normal'
AND prior.input_event_sequence=frontier.sequence),1),
coalesce(nullif($4::uuid,'00000000-0000-0000-0000-000000000000'),source.agent_config_id),
coalesce(nullif($2::uuid,'00000000-0000-0000-0000-000000000000'),source.configured_model_revision_id),
frontier.sequence,$3,'started',statement_timestamp()
FROM model_call_contexts source
CROSS JOIN LATERAL (SELECT max(sequence) AS sequence FROM agent_events WHERE agent_id=source.agent_id) frontier
WHERE source.id=$1 RETURNING id`, source, revision, fixture.Lock.ID, config).Scan(&id))
	row, found, err := fixture.Store.Execution().GetModelCallContext(ctx, testProjectID, fixture.AgentID, id)
	require.NoError(t, err)
	require.True(t, found)
	return row
}
