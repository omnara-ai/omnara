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

func TestNormalCompletionPersistsRequestIdentityAndChecksReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		toolResponse bool
		usage        modelenvelope.Usage
	}{
		{"text_with_usage", false, modelenvelope.Usage{InputTokens: 120, UncachedInputTokens: 120, OutputTokens: 30}},
		{"tool_without_usage", true, modelenvelope.Usage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "input_identity_"+tc.name)
			slug := modelProviderSlugForContext(
				t,
				ctx,
				fixture.Store,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			)
			identity := &modelenvelope.RequestInputIdentity{Fingerprint: strings.Repeat("a", 64), ItemCount: 2}

			envelope := modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: slug,
				ServedProviderModelSlug:    slug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "identity-response",
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
						ProviderCallID: "identity-call",
						ToolName:       "read_file",
						ToolInput: json.RawMessage(
							`{"path":"/artifacts/invalid"}`,
						),
					},
				}
			}
			complete := func(value *modelenvelope.RequestInputIdentity) error {
				if tc.toolResponse {
					_, _, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
						ctx,
						executionstore.RecordToolCallSourceAndCompleteContextInput{
							ProjectID:            testProjectID,
							AgentID:              fixture.AgentID,
							RuntimeLockID:        fixture.Lock.ID,
							ModelCallContextID:   claim.Context.ID,
							ProviderResponse:     envelope,
							RequestInputIdentity: value,
							ToolCallBindings: []executionstore.ToolCallBindingInput{
								{
									ProviderCallID: "identity-call",
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
						ModelCallContextID: claim.Context.ID, ProviderResponse: envelope, RequestInputIdentity: value,
					},
				)
				return err
			}
			require.NoError(t, complete(identity))
			require.NoError(t, complete(identity))
			changed := *identity
			changed.Fingerprint = strings.Repeat("d", 64)
			require.ErrorIs(t, complete(&changed), storeerr.ErrIdempotencyConflict)
			record, found, err := fixture.Store.Execution().GetModelCallContext(
				ctx,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, identity, record.RequestInputIdentity)
			require.Equal(t, envelope.Normalized.Usage, record.Usage)
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
	failure := executionstore.RecordCompactionFailureAndResumeNormalInput{
		Outcome:   executionstore.OptionalCompactionIneffective,
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: optional.CompactionCall.Context.ID,
		APIFormat:          modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		ErrorKind: modelprotocol.ErrorKindTransient, ErrorCode: "summary_not_reduced",
		ErrorMessage: "optional summary ineffective",
		Usage:        modelenvelope.Usage{InputTokens: 200, UncachedInputTokens: 200, OutputTokens: 10},
	}
	failed, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
	require.NoError(t, err)
	require.Equal(t, executionstore.ModelCallRecoveryResumeNormal, failed.RecoveryKind)
	require.Equal(t, executionstore.OptionalCompactionIneffective, failed.OptionalCompactionOutcome)
	require.Nil(t, failed.RequestInputIdentity)
	_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
	require.NoError(t, err)
	changed := failure
	changed.ErrorMessage = "different evidence"
	_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, changed)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	changed = failure
	changed.Outcome = executionstore.OptionalCompactionInterrupted
	_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, changed)
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
	require.True(t, state.LastOptionalCompactionNeedsHeadroom)
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
	require.Equal(t, executionstore.ModelCallRecoveryResumeNormal, child.RecoveryKind)
	require.Equal(t, executionstore.OptionalCompactionInterrupted, child.OptionalCompactionOutcome)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, child.ID,
	)
	require.NoError(t, err)
	require.False(t, state.LastOptionalCompactionNeedsHeadroom)
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
	budget := 4096
	replacement, err := fixture.Store.Execution().ReplaceCompactionSource(
		ctx,
		executionstore.ReplaceCompactionSourceInput{
			ProjectID:                  testProjectID,
			AgentID:                    fixture.AgentID,
			RuntimeLockID:              fixture.Lock.ID,
			ModelCallContextID:         claim.Context.ID,
			ErrorKind:                  modelprotocol.ErrorKindContextWindow,
			ErrorMessage:               "source too large",
			NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd,
			NextSourceExcerptBytes:     &budget,
		},
	)
	require.NoError(t, err)
	require.True(t, replacement.CompactionCall.Claimed)
	require.Equal(t, &budget, replacement.CompactionCall.Context.SourceExcerptBytes)
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
		ErrorMessage:               "no reduction",
		NextSourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd,
		NextSourceExcerptBytes:     &budget,
	})
	require.True(t, errors.Is(err, storeerr.ErrStateTransitionConflict))
}

func TestOptionalCompactionObservationsResetAtAttemptAndModelRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_observation_scope")
	optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	_, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx,
		executionstore.RecordCompactionFailureAndResumeNormalInput{
			Outcome:   executionstore.OptionalCompactionIneffective,
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
	later := insertProjectionNormalContext(t, fixture, next.Context.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, later.ID)
	require.NoError(t, err)
	require.Equal(t, 100, state.MinimumObservedNormalInputTokens)
	require.Equal(t, 100, state.LatestObservedNormalInputTokens)
	_, err = fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='compact_optional',
error_kind='context_window',error_message='later optional',completed_at=statement_timestamp() WHERE id=$1`, later.ID)
	require.NoError(t, err)
	afterOptional := insertProjectionNormalContext(t, fixture, later.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, afterOptional.ID)
	require.NoError(t, err)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.True(t, state.HasPriorOptionalCompaction)
	require.False(t, state.LastOptionalCompactionNeedsHeadroom)
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
	changed := insertProjectionNormalContext(t, fixture, afterOptional.ID, updated.CurrentRevisionID, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, changed.ID)
	require.NoError(t, err)
	require.False(t, state.HasPriorOptionalCompaction)
	require.Zero(t, state.LatestObservedNormalInputTokens)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
}

func TestRequestIdentityDatabaseGuardsRejectPartialOrFailureEvidence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "identity_guards")
	for _, fields := range []string{
		`state='succeeded',api_format='openai_responses',api_variant='default',
request_input_fingerprint=repeat('a',64)`,
		`state='failed',recovery_kind='retry',retry_at=statement_timestamp(),
error_kind='runtime',error_message='test',request_input_fingerprint=repeat('a',64),request_input_item_count=1`,
	} {
		_, err := fixture.Store.pool.Exec(ctx,
			`UPDATE model_call_contexts SET completed_at=statement_timestamp(),`+fields+` WHERE id=$1`, claim.Context.ID)
		assertPgConstraint(t, err, "23514", "model_call_contexts_request_input_identity")
	}
}

func TestOptionalCompactionOutcomeGuards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_outcome_guards")
	optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	for _, tc := range []struct {
		recovery string
		outcome  *string
	}{
		{"resume_normal", nil},
		{"resume_normal", new("future_outcome")},
		{"retry", new("interrupted")},
	} {
		_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind=$2,optional_compaction_outcome=$3,
retry_at=CASE WHEN $2='retry' THEN statement_timestamp() ELSE NULL END,
error_kind='runtime',error_message='optional outcome guard',completed_at=statement_timestamp() WHERE id=$1`,
			optional.CompactionCall.Context.ID, tc.recovery, tc.outcome,
		)
		assertPgConstraint(t, err, "23514", "model_call_contexts_optional_outcome")
	}
	for _, outcome := range []executionstore.OptionalCompactionOutcome{"", "future_outcome"} {
		_, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(
			ctx, executionstore.RecordCompactionFailureAndResumeNormalInput{
				Outcome: outcome, ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: optional.CompactionCall.Context.ID,
				ErrorKind:          modelprotocol.ErrorKindTransient, ErrorMessage: "optional outcome guard",
			},
		)
		require.ErrorContains(t, err, "valid optional compaction outcome")
	}
}

func TestObservedCompactionPressureDoesNotResurrectUsageAfterUnknownSuccess(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "observed_pressure")
	optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
	_, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(
		ctx, executionstore.RecordCompactionFailureAndResumeNormalInput{
			Outcome:   executionstore.OptionalCompactionInterrupted,
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
		current := insertProjectionNormalContext(t, fixture, claim.Context.ID, uuid.Nil, uuid.Nil)
		state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
		require.NoError(t, err)
		require.Equal(t, inputTokens, state.LatestObservedNormalInputTokens)
		require.Equal(t, observation.minimum, state.MinimumObservedNormalInputTokens)
		require.False(t, state.LastOptionalCompactionNeedsHeadroom)
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
	changed := insertProjectionNormalContext(t, fixture, claim.Context.ID, uuid.Nil, config.ID)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, changed.ID)
	require.NoError(t, err)
	require.Zero(t, state.MinimumObservedNormalInputTokens)
	require.Zero(t, state.LatestObservedNormalInputTokens)
}
