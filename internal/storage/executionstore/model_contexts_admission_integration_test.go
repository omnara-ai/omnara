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
	for _, toolResponse := range []bool{false, true} {
		for _, withUsage := range []bool{false, true} {
			name := "text"
			if toolResponse {
				name = "tool"
			}
			if withUsage {
				name += "_usage"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "input_identity_"+name)
				slug := modelProviderSlugForContext(
					t,
					ctx,
					fixture.Store,
					testProjectID,
					fixture.AgentID,
					claim.Context.ID,
				)
				identity := &modelenvelope.RequestInputIdentity{
					Version:   1,
					ItemCount: 2,
					RouteFingerprint: strings.Repeat(
						"a",
						64,
					),
					StaticFingerprint: strings.Repeat(
						"b",
						64,
					),
					PrefixFingerprint: strings.Repeat(
						"c",
						64,
					),
				}
				envelope := modelenvelope.ResponseEnvelope{
					RequestedProviderModelSlug: slug,
					ServedProviderModelSlug:    slug,
					APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
					APIVariant:                 modelprotocol.APIVariantDefault,
					Normalized: modelenvelope.ResponseNormalized{
						ID:         "identity-response",
						StopReason: modelenvelope.StopReasonEndTurn,
						Content: []modelenvelope.ResponsePart{
							{
								Type: modelenvelope.ResponsePartTypeText,
								Text: "done",
							},
						},
					},
				}
				if withUsage {
					envelope.Normalized.Usage = modelenvelope.Usage{
						InputTokens:         120,
						UncachedInputTokens: 120,
						OutputTokens:        30,
					}
				}
				if toolResponse {
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
					if toolResponse {
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
				changed.PrefixFingerprint = strings.Repeat("d", 64)
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
				events, err := fixture.Store.Execution().ListContextEvents(
					ctx,
					testProjectID,
					fixture.AgentID,
					0,
					claim.Context.InputEventSequence+10,
					100,
				)
				require.NoError(t, err)
				var measured *executionstore.ContextEventRecord
				for index := range events {
					if events[index].ModelCallContextID == claim.Context.ID {
						measured = &events[index]
					}
				}
				require.NotNil(t, measured)
				require.Equal(t, identity, measured.RequestInputIdentity)
				require.Equal(t, record.Usage, measured.Usage)
				require.Equal(t, slug, measured.ServedProviderModelSlug)
				if !withUsage {
					require.Equal(t, modelenvelope.Usage{}, measured.Usage)
				}
			})
		}
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
	var target *int
	if optional {
		kind = executionstore.ModelCallRecoveryCompactOptional
		target = new(32000)
	}
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID, SourceEventSequenceEnd: sourceEnd,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: parent.Context.ID, RecoveryKind: kind,
				OptionalInputTargetTokens: target,
				ErrorKind:                 modelprotocol.ErrorKindContextWindow,
				ErrorCode:                 "test_overflow", ErrorMessage: "test overflow",
			},
		},
	)
	require.NoError(t, err)
	require.True(t, handoff.CompactionCall.Claimed)
	require.Equal(t, parent.Context.ID, handoff.CompactionCall.Context.ParentNormalModelCallContextID)
	require.Equal(t, target, handoff.ParentContext.OptionalInputTargetTokens)
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
		ErrorKind: "invalid_response", ErrorCode: "refusal", ErrorMessage: "optional summary refused",
		Usage: modelenvelope.Usage{InputTokens: 200, UncachedInputTokens: 200, OutputTokens: 10},
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
	require.Equal(t, 32000, state.LastOptionalInputTargetTokens)
	require.True(t, state.LastOptionalCompactionNeedsHeadroom)
	require.Zero(t, state.NormalRetryCount)
	require.Zero(t, state.ProviderAttemptCount)
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

func TestOptionalCompactionHeadroomDependsOnActualOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind          modelprotocol.ErrorKind
		code          string
		needsHeadroom bool
	}{
		{modelprotocol.ErrorKindRateLimit, "rate_limit_exceeded", false},
		{modelprotocol.ErrorKindProviderUnavailable, "overloaded_error", false},
		{modelprotocol.ErrorKindTransient, "provider_idle_timeout", false},
		{modelprotocol.ErrorKindTransient, "load_compaction_source_failed", false},
		{modelprotocol.ErrorKindRuntime, storeerr.ManagedWorkAdmissionDeniedCode, false},
		{modelprotocol.ErrorKindInvalidRequest, "refusal", true},
		{modelprotocol.ErrorKindTransient, "summary_not_reduced", true},
		{modelprotocol.ErrorKindTransient, "summary_truncated", true},
		{modelprotocol.ErrorKindTransient, "empty_summary", true},
		{modelprotocol.ErrorKindTransient, "tool_use", true},
		{modelprotocol.ErrorKindUnknown, "malformed_success_response", true},
		{modelprotocol.ErrorKindContextWindow, "compaction_source_irreducible", true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_outcome_"+tc.code)
			optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
			outcome := executionstore.OptionalCompactionInterrupted
			if tc.needsHeadroom {
				outcome = executionstore.OptionalCompactionIneffective
			}
			_, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(
				ctx, executionstore.RecordCompactionFailureAndResumeNormalInput{
					Outcome:   outcome,
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					ModelCallContextID: optional.CompactionCall.Context.ID,
					ErrorKind:          tc.kind, ErrorCode: tc.code, ErrorMessage: "optional attempt failed",
				},
			)
			require.NoError(t, err)
			next, err := fixture.Store.Execution().ClaimNextModelCallContext(
				ctx, executionstore.ClaimNextModelCallContextInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					PredecessorModelCallContextID: parent.Context.ID,
				},
			)
			require.NoError(t, err)
			require.True(t, next.Claimed)
			state, err := fixture.Store.Execution().GetModelCallRecoveryState(
				ctx, testProjectID, fixture.AgentID, next.Context.ID,
			)
			require.NoError(t, err)
			require.Equal(t, tc.needsHeadroom, state.LastOptionalCompactionNeedsHeadroom)
			require.True(t, state.OptionalCompactionAttemptedAtFrontier)
		})
	}
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
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx,
		testProjectID,
		fixture.AgentID,
		replacement.CompactionCall.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.MaxModelCallRetriesPerOperation, state.CompactionRetryCount)
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

func TestOptionalCompactionRearmUsesObservedNormalInputWithoutRequestIdentity(t *testing.T) {
	t.Parallel()
	for _, measured := range []bool{false, true} {
		name := "missing_usage"
		if measured {
			name = "measured_usage"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "headroom_"+name)
			optional := admissionHandoff(t, ctx, fixture, parent, true, parent.Context.InputEventSequence)
			_, err := fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(
				ctx,
				executionstore.RecordCompactionFailureAndResumeNormalInput{
					Outcome:            executionstore.OptionalCompactionIneffective,
					ProjectID:          testProjectID,
					AgentID:            fixture.AgentID,
					RuntimeLockID:      fixture.Lock.ID,
					ModelCallContextID: optional.CompactionCall.Context.ID,
					ErrorKind:          "invalid_response",
					ErrorMessage:       "optional refusal",
					APIFormat:          modelprotocol.APIFormatOpenAIResponses,
					APIVariant:         modelprotocol.APIVariantDefault,
					Usage:              modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10},
				},
			)
			require.NoError(t, err)
			revision := parent.Context.ConfiguredModelRevisionID
			headroom, err := fixture.Store.Execution().HasObservedInputHeadroomSince(
				ctx,
				testProjectID,
				fixture.AgentID,
				parent.Context.ID,
				revision,
				1000,
			)
			require.NoError(t, err)
			require.False(t, headroom)
			next, err := fixture.Store.Execution().ClaimNextModelCallContext(
				ctx,
				executionstore.ClaimNextModelCallContextInput{
					ProjectID:                     testProjectID,
					AgentID:                       fixture.AgentID,
					RuntimeLockID:                 fixture.Lock.ID,
					PredecessorModelCallContextID: parent.Context.ID,
				},
			)
			require.NoError(t, err)
			_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
				ctx,
				executionstore.RecordRecoverableModelCallFailureInput{
					ProjectID:          testProjectID,
					AgentID:            fixture.AgentID,
					RuntimeLockID:      fixture.Lock.ID,
					ModelCallContextID: next.Context.ID,
					ErrorKind:          modelprotocol.ErrorKindProviderUnavailable,
					ErrorMessage:       "provider failure with usage",
					APIFormat:          modelprotocol.APIFormatOpenAIResponses,
					APIVariant:         modelprotocol.APIVariantDefault,
					Usage:              modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10},
				},
			)
			require.NoError(t, err)
			headroom, err = fixture.Store.Execution().HasObservedInputHeadroomSince(
				ctx,
				testProjectID,
				fixture.AgentID,
				parent.Context.ID,
				revision,
				1000,
			)
			require.NoError(t, err)
			require.False(t, headroom)
			next, err = fixture.Store.Execution().ClaimNextModelCallContext(
				ctx,
				executionstore.ClaimNextModelCallContextInput{
					ProjectID:                     testProjectID,
					AgentID:                       fixture.AgentID,
					RuntimeLockID:                 fixture.Lock.ID,
					PredecessorModelCallContextID: next.Context.ID,
				},
			)
			require.NoError(t, err)
			slug := modelProviderSlugForContext(t, ctx, fixture.Store, testProjectID, fixture.AgentID, next.Context.ID)
			usage := modelenvelope.Usage{}
			if measured {
				usage = modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10}
			}
			_, err = fixture.Store.Execution().RecordModelOutputAndCompleteContext(
				ctx,
				executionstore.RecordModelOutputAndCompleteContextInput{
					ProjectID:          testProjectID,
					AgentID:            fixture.AgentID,
					RuntimeLockID:      fixture.Lock.ID,
					ModelCallContextID: next.Context.ID,
					ProviderResponse: modelenvelope.ResponseEnvelope{
						RequestedProviderModelSlug: slug,
						ServedProviderModelSlug:    slug,
						APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
						APIVariant:                 modelprotocol.APIVariantDefault,
						Normalized: modelenvelope.ResponseNormalized{
							ID:         "headroom-response",
							Usage:      usage,
							StopReason: modelenvelope.StopReasonEndTurn,
							Content: []modelenvelope.ResponsePart{
								{
									Type: modelenvelope.ResponsePartTypeText,
									Text: "done",
								},
							},
						},
					},
				},
			)
			require.NoError(t, err)
			for _, tc := range []struct {
				revision uuid.UUID
				limit    int
				want     bool
			}{
				{revision, 100, measured}, {revision, 99, false}, {uuid.New(), 1000, false},
			} {
				headroom, err = fixture.Store.Execution().HasObservedInputHeadroomSince(
					ctx,
					testProjectID,
					fixture.AgentID,
					parent.Context.ID,
					tc.revision,
					tc.limit,
				)
				require.NoError(t, err)
				require.Equal(t, tc.want, headroom)
			}
			var laterOptional uuid.UUID
			require.NoError(t, fixture.Store.pool.QueryRow(ctx, `INSERT INTO model_call_contexts(
org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
SELECT org_id,project_id,agent_id,'normal',1,agent_config_id,configured_model_revision_id,
input_event_sequence+100,runtime_lock_id,'started',statement_timestamp()
FROM model_call_contexts WHERE id=$1 RETURNING id`, next.Context.ID).Scan(&laterOptional))
			state, err := fixture.Store.Execution().GetModelCallRecoveryState(
				ctx, testProjectID, fixture.AgentID, laterOptional,
			)
			require.NoError(t, err)
			require.Equal(t, usage.InputTokens, state.LatestObservedNormalInputTokens)
			require.Equal(t, parent.Context.ID, state.LastOptionalContextID)
			_, err = fixture.Store.pool.Exec(
				ctx,
				`UPDATE model_call_contexts
SET state='failed',recovery_kind='compact_optional',error_kind='context_window',
optional_input_target_tokens=16000,error_message='later optional',completed_at=statement_timestamp() WHERE id=$1`,
				laterOptional,
			)
			require.NoError(t, err)
			headroom, err = fixture.Store.Execution().HasObservedInputHeadroomSince(
				ctx,
				testProjectID,
				fixture.AgentID,
				laterOptional,
				revision,
				1000,
			)
			require.NoError(t, err)
			require.False(t, headroom)

			original, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(ctx, testOrgID, revision)
			require.NoError(t, err)
			window := original.ContextWindowTokens + 1
			updated, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
				OrgID: testOrgID, ModelProviderConfigID: original.ModelProviderConfigID,
				ID: original.ConfiguredModelID, ContextWindowTokens: &window,
			})
			require.NoError(t, err)
			var changedRevisionCall uuid.UUID
			require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
SELECT org_id,project_id,agent_id,'normal',attempt_number+1,agent_config_id,
$2,input_event_sequence,runtime_lock_id,'started',statement_timestamp()
FROM model_call_contexts WHERE id=$1 RETURNING id`,
				laterOptional, updated.CurrentRevisionID,
			).Scan(&changedRevisionCall))
			state, err = fixture.Store.Execution().GetModelCallRecoveryState(
				ctx, testProjectID, fixture.AgentID, changedRevisionCall,
			)
			require.NoError(t, err)
			require.Equal(t, uuid.Nil, state.LastOptionalContextID)
			require.Zero(t, state.LatestObservedNormalInputTokens)
			require.True(t, state.OptionalCompactionAttemptedAtFrontier)
		})
	}
}

func TestRequestIdentityDatabaseGuardsRejectPartialOrFailureEvidence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "identity_guards")
	for _, fields := range []string{
		"request_input_version=1",
		`request_input_version=1,request_input_route_fingerprint=repeat('a',64),
request_input_static_fingerprint=repeat('b',64),request_input_prefix_fingerprint=repeat('c',64),
request_input_item_count=1`,
	} {
		_, err := fixture.Store.pool.Exec(
			ctx,
			`UPDATE model_call_contexts
SET state='failed',recovery_kind='retry',retry_at=statement_timestamp(),
error_kind='runtime',error_message='test',completed_at=statement_timestamp(),`+fields+` WHERE id=$1`,
			claim.Context.ID,
		)
		assertPgConstraint(t, err, "23514", "model_call_contexts_request_input_identity")
	}
}

func TestOptionalCompactionInputTargetGuards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "optional_target_guards")
	for _, tc := range []struct {
		kind   executionstore.ModelCallRecoveryKind
		target *int
	}{
		{executionstore.ModelCallRecoveryCompactOptional, nil},
		{executionstore.ModelCallRecoveryCompactOptional, new(0)},
		{executionstore.ModelCallRecoveryCompactOptional, new(-1)},
		{executionstore.ModelCallRecoveryCompact, new(32000)},
	} {
		_, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
			ctx, executionstore.RecordModelCallFailureAndClaimCompactionInput{
				ParentContextID: claim.Context.ID, SourceEventSequenceEnd: claim.Context.InputEventSequence,
				Failure: executionstore.RecordRecoverableModelCallFailureInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					ModelCallContextID: claim.Context.ID, RecoveryKind: tc.kind,
					OptionalInputTargetTokens: tc.target,
					ErrorKind:                 modelprotocol.ErrorKindContextWindow, ErrorMessage: "invalid optional target",
				},
			},
		)
		require.Error(t, err)
		_, err = fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind=$2,optional_input_target_tokens=$3,error_kind='context_window',
error_message='invalid optional target',completed_at=statement_timestamp() WHERE id=$1`,
			claim.Context.ID, string(tc.kind), tc.target,
		)
		assertPgConstraint(t, err, "23514", "model_call_contexts_optional_input_target")
	}
	admissionHandoff(t, ctx, fixture, claim, true, claim.Context.InputEventSequence)
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

func TestOptionalCompactionWithoutSummaryAttemptDoesNotRequireHeadroom(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, parent := newStartedNormalModelCallTestFixture(t, ctx, "optional_without_child")
	_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='compact_optional',optional_input_target_tokens=32000,
error_kind='context_window',error_message='preempted optional',completed_at=statement_timestamp() WHERE id=$1`,
		parent.Context.ID,
	)
	require.NoError(t, err)
	var currentID uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `INSERT INTO model_call_contexts(
org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
SELECT org_id,project_id,agent_id,'normal',attempt_number+1,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,'started',statement_timestamp()
FROM model_call_contexts WHERE id=$1 RETURNING id`, parent.Context.ID).Scan(&currentID))
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, currentID)
	require.NoError(t, err)
	require.Equal(t, parent.Context.ID, state.LastOptionalContextID)
	require.False(t, state.LastOptionalCompactionNeedsHeadroom)
	require.True(t, state.OptionalCompactionAttemptedAtFrontier)
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
			ErrorKind:          modelprotocol.ErrorKindProviderUnavailable, ErrorMessage: "temporary summary failure",
		},
	)
	require.NoError(t, err)
	claim, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: parent.Context.ID,
	})
	require.NoError(t, err)
	for _, inputTokens := range []int{2000, 0} {
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
		var nextID uuid.UUID
		require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
SELECT org_id,project_id,agent_id,'normal',1,agent_config_id,configured_model_revision_id,
(SELECT max(sequence) FROM agent_events WHERE agent_id=context.agent_id),
runtime_lock_id,'started',statement_timestamp()
FROM model_call_contexts context WHERE id=$1 RETURNING id`, claim.Context.ID).Scan(&nextID))
		state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, nextID)
		require.NoError(t, err)
		require.Equal(t, inputTokens, state.LatestObservedNormalInputTokens)
		require.Equal(t, parent.Context.ID, state.LastOptionalContextID)
		contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx, testProjectID, fixture.AgentID, nextID,
		)
		require.NoError(t, err)
		require.True(t, found)
		claim.Context = contextRecord
	}
}

func TestRecoveryOutputAllowanceDoesNotCarryAcrossModelRevisions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "output_cap_revision")
	oldRevision, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(
		ctx, testOrgID, claim.Context.ConfiguredModelRevisionID,
	)
	require.NoError(t, err)
	recordFailure := func(limit *int) {
		t.Helper()
		_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
			ctx, executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindContextWindow,
				ErrorMessage: "reduce output allowance", RecoveryMaxOutputTokens: limit,
			},
		)
		require.NoError(t, err)
	}
	claimNext := func() {
		t.Helper()
		next, err := fixture.Store.Execution().ClaimNextModelCallContext(
			ctx, executionstore.ClaimNextModelCallContextInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				PredecessorModelCallContextID: claim.Context.ID,
			},
		)
		require.NoError(t, err)
		require.True(t, next.Claimed)
		claim = next
	}
	smallLimit := 1
	recordFailure(&smallLimit)
	claimNext()
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, &smallLimit, state.RecoveryMaxOutputTokens)
	recordFailure(nil)

	contextWindow := oldRevision.ContextWindowTokens + 1
	updated, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: testOrgID, ModelProviderConfigID: oldRevision.ModelProviderConfigID,
		ID: oldRevision.ConfiguredModelID, ContextWindowTokens: &contextWindow,
	})
	require.NoError(t, err)
	claimNext()
	require.Equal(t, updated.CurrentRevisionID, claim.Context.ConfiguredModelRevisionID)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.Nil(t, state.RecoveryMaxOutputTokens)
	newLimit := 1024
	recordFailure(&newLimit)
	claimNext()
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, claim.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, &newLimit, state.RecoveryMaxOutputTokens)
}
