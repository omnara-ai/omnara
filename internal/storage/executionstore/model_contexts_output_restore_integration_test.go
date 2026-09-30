//go:build integration

package executionstore_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func outputRestorationFailure(
	fixture processDaemonFixture,
	contextID uuid.UUID,
) executionstore.RecordRecoverableModelCallFailureInput {
	return executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: contextID, RecoveryKind: executionstore.ModelCallRecoveryRestoreOutput,
		APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		ErrorKind: modelprotocol.ErrorKindTransient, ErrorMessage: "reduced output produced no progress",
		ProviderResponseID: "empty-response",
	}
}

func claimAfterOutputRestoration(
	t *testing.T,
	fixture processDaemonFixture,
	contextID uuid.UUID,
) executionstore.ModelCallContextRecord {
	t.Helper()
	next, err := fixture.Store.Execution().ClaimNextModelCallContext(t.Context(),
		executionstore.ClaimNextModelCallContextInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			PredecessorModelCallContextID: contextID,
		})
	require.NoError(t, err)
	require.True(t, next.Claimed)
	return next.Context
}

func TestOutputRestorationSchedulesOnceWithoutResettingTransientBudget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "restore_output_retry")
	input := outputRestorationFailure(fixture, claim.Context.ID)
	failed, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.ModelCallRecoveryRestoreOutput, failed.RecoveryKind)
	require.NotNil(t, failed.RetryAt)
	work, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, failed.ID, work.ModelCallContextID)
	current := claimAfterOutputRestoration(t, fixture, failed.ID)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.True(t, state.OutputAllowanceRestored)
	require.Zero(t, state.NormalRetryCount)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, outputRestorationFailure(fixture, current.ID))
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='restore_output',retry_at=statement_timestamp(),
error_kind='transient',error_message='second restoration',api_format='openai_responses',api_variant='default',
completed_at=statement_timestamp() WHERE id=$1`, current.ID)
	require.ErrorContains(t, err, "only be restored once")

	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
	))
	fixture.Lock, err = fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, testWorkerProcessID, time.Minute,
	)
	require.NoError(t, err)
	current = claimAfterOutputRestoration(t, fixture, current.ID)
	for used := 1; used < executionstore.MaxModelCallRetriesPerOperation; used++ {
		state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
		require.NoError(t, err)
		require.True(t, state.OutputAllowanceRestored)
		require.Equal(t, used, state.NormalRetryCount)
		retry := outputRestorationFailure(fixture, current.ID)
		retry.RecoveryKind = executionstore.ModelCallRecoveryRetry
		_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, retry)
		require.NoError(t, err)
		current = claimAfterOutputRestoration(t, fixture, current.ID)
	}
	retry := outputRestorationFailure(fixture, current.ID)
	retry.RecoveryKind = executionstore.ModelCallRecoveryRetry
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, retry)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
	))
	fixture.Lock, err = fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, testWorkerProcessID, time.Minute,
	)
	require.NoError(t, err)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.True(t, state.OutputAllowanceRestored, "a synthetic terminal error is not productive output")
}

func TestOutputRestorationSurvivesCheckpointReplacementAndOutputLimitContinuation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, checkpoint, current := checkpointProjectionFixture(t, "restore_output_checkpoint")
	_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, outputRestorationFailure(fixture, current.ID))
	require.NoError(t, err)
	current = claimAfterOutputRestoration(t, fixture, current.ID)
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx, executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: current.ID, ReplacesCheckpointID: checkpoint.ID,
			SourceEventSequenceEnd: checkpoint.SummarizedThroughEventSequence,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: current.ID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
				ErrorKind: modelprotocol.ErrorKindContextWindow, ErrorMessage: "normal input overflow",
			},
		})
	require.NoError(t, err)
	replacement, err := fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: handoff.CompactionCall.Context.ID,
		ErrorKind:          modelprotocol.ErrorKindContextWindow, ErrorMessage: "summary source overflow",
		NextSourceEventSequenceEnd: checkpoint.SummarizedThroughEventSequence, NextSourceExcerptBytes: new(512),
	})
	require.NoError(t, err)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, replacement.CompactionCall.Context.ID,
	)
	require.NoError(t, err)
	require.True(t, state.OutputAllowanceRestored)
	_, err = publishCheckpointForRangeTest(t, ctx, fixture, replacement.CompactionCall, "smaller old memory", fixture.Now)
	require.NoError(t, err)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.True(t, state.OutputAllowanceRestored)
	createModelOutputEventForTurnTest(t, ctx, fixture, uuid.Nil, current.ID,
		"restore_output_max_tokens", "max_tokens", "", fixture.Now)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.True(t, state.OutputAllowanceRestored)
}

func TestOutputRestorationResetsAfterProgressOrConfigurationChange(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"input", "productive_output", "model_revision", "agent_config"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "restore_reset_"+change)
			_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
				ctx, outputRestorationFailure(fixture, claim.Context.ID),
			)
			require.NoError(t, err)
			current := claim.Context
			var revisionID, configID uuid.UUID
			switch change {
			case "input":
				_, _, _, err = fixture.Store.Execution().CreateAgentContentInput(ctx,
					executionstore.CreateAgentContentInputInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
						ContentBlocks: json.RawMessage(`[{"type":"text","text":"a fresh input"}]`),
						DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: "restore_reset_input",
					})
				require.NoError(t, err)
				_, found := admitNextAgentInputAndOpenTurnForTest(
					t, ctx, fixture.Store, testProjectID, fixture.AgentID, fixture.Lock.ID,
				)
				require.True(t, found)
			case "productive_output":
				current = claimAfterOutputRestoration(t, fixture, current.ID)
				createModelOutputEventForTurnTest(t, ctx, fixture, uuid.Nil, current.ID,
					"restore_reset_productive", "end_turn", "", fixture.Now)
			case "model_revision":
				revision, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(
					ctx, testOrgID, current.ConfiguredModelRevisionID,
				)
				require.NoError(t, err)
				updated, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
					OrgID: testOrgID, ModelProviderConfigID: revision.ModelProviderConfigID, ID: revision.ConfiguredModelID,
					ContextWindowTokens: new(revision.ContextWindowTokens + 1),
				})
				require.NoError(t, err)
				revisionID = updated.CurrentRevisionID
			case "agent_config":
				config := mustCreateAgentConfigFromYAML(t, ctx, fixture.Store,
					strings.Replace(testAgentConfigYAML(), "instruction: test", "instruction: changed output setup", 1))
				configID = config.ID
			}
			current = insertProjectionNormalContext(t, fixture, current.ID, revisionID, configID)
			state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
			require.NoError(t, err)
			require.False(t, state.OutputAllowanceRestored)
			_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
				ctx, outputRestorationFailure(fixture, current.ID),
			)
			require.NoError(t, err)
		})
	}
}

func TestOutputRestorationRequiresProviderEvidenceAndExclusiveAction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "restore_output_guards")
	for _, invalid := range []func(*executionstore.RecordRecoverableModelCallFailureInput){
		func(input *executionstore.RecordRecoverableModelCallFailureInput) { input.APIFormat = "" },
		func(input *executionstore.RecordRecoverableModelCallFailureInput) {
			input.ErrorKind = modelprotocol.ErrorKindRuntime
		},
		func(input *executionstore.RecordRecoverableModelCallFailureInput) {
			input.RecoveryMaxOutputTokens = new(1)
		},
		func(input *executionstore.RecordRecoverableModelCallFailureInput) {
			input.RecoveryCheckpointRetainedBytes = new(0)
		},
	} {
		input := outputRestorationFailure(fixture, claim.Context.ID)
		invalid(&input)
		_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, input)
		require.Error(t, err)
	}
	for _, kind := range []string{"runtime", "context_window"} {
		_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='restore_output',retry_at=statement_timestamp(),
error_kind=$2,error_message='invalid restoration',api_format='openai_responses',api_variant='default',
completed_at=statement_timestamp() WHERE id=$1`, claim.Context.ID, kind)
		assertPgConstraint(t, err, "23514", "model_call_contexts_restore_output")
	}
}
