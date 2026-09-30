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

func checkpointProjectionFixture(
	t *testing.T,
	name string,
) (processDaemonFixture, executionstore.ContextCheckpointRecord, executionstore.ModelCallContextRecord) {
	t.Helper()
	ctx := t.Context()
	fixture, _, initial := newStartedNormalModelCallTestFixture(t, ctx, name)
	_, output := createModelOutputEventForTurnTest(
		t, ctx, fixture, uuid.Nil, initial.Context.ID, name, "end_turn", "", fixture.Now,
	)
	claim := claimSentCompactionForRangeTest(t, ctx, fixture, 1, output.Event.Sequence, output.Event.Sequence, fixture.Now)
	checkpoint, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, strings.Repeat("summary ", 512), fixture.Now)
	require.NoError(t, err)
	return fixture, checkpoint, insertProjectionNormalContext(t, fixture, claim.Context.ID, uuid.Nil, uuid.Nil)
}

func insertProjectionNormalContext(
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

func checkpointProjectionFailure(
	fixture processDaemonFixture,
	current executionstore.ModelCallContextRecord,
	retained int,
) executionstore.RecordRecoverableModelCallFailureInput {
	return executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: current.ID, RecoveryKind: executionstore.ModelCallRecoveryRetry,
		APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		ErrorKind: modelprotocol.ErrorKindContextWindow, ErrorMessage: "provider input exceeds context",
		RecoveryCheckpointRetainedBytes: &retained,
	}
}

func TestCheckpointProjectionSurvivesRestartAndDecreasesThroughMarkerOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, checkpoint, current := checkpointProjectionFixture(t, "projection_restart")
	failure := checkpointProjectionFailure(fixture, current, 512)
	failure.RecoveryMaxOutputTokens = new(32)
	finished, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, failure)
	require.NoError(t, err)
	require.Equal(t, new(512), finished.RecoveryCheckpointRetainedBytes)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
	))
	fixture.Lock, err = fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, testWorkerProcessID, time.Minute,
	)
	require.NoError(t, err)
	for _, retained := range []int{256, 128, 64, 32, 16, 8, 4, 2, 1, 0} {
		next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			PredecessorModelCallContextID: current.ID,
		})
		require.NoError(t, err)
		require.True(t, next.Claimed)
		current = next.Context
		state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
		require.NoError(t, err)
		require.Equal(t, checkpoint.ID, state.RecoveryCheckpointID)
		require.Equal(t, failure.RecoveryCheckpointRetainedBytes, state.RecoveryCheckpointRetainedBytes)
		require.Equal(t, new(32), state.RecoveryMaxOutputTokens)
		require.Zero(t, state.RetryCount)
		_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
			checkpointProjectionFailure(fixture, current, *state.RecoveryCheckpointRetainedBytes))
		assertPgErrorMessage(t, err, "23514", "recovery checkpoint retained bytes must decrease")
		failure = checkpointProjectionFailure(fixture, current, retained)
		_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, failure)
		require.NoError(t, err)
	}
	next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: current.ID,
	})
	require.NoError(t, err)
	require.True(t, next.Claimed)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, next.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, new(0), state.RecoveryCheckpointRetainedBytes)
	stored, found, err := fixture.Store.Execution().GetContextCheckpoint(
		ctx, testProjectID, fixture.AgentID, checkpoint.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, checkpoint.Summary, stored.Summary)
}

func TestCheckpointProjectionCarriesAcrossTurnsAndResetsForModelRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, checkpoint, current := checkpointProjectionFixture(t, "projection_revision")
	_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx, checkpointProjectionFailure(fixture, current, 0),
	)
	require.NoError(t, err)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	createModelOutputEventForTurnTest(t, ctx, fixture, uuid.Nil, current.ID,
		"projection_revision_done", "end_turn", "", fixture.Now)
	_, _, _, err = fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"continue with new work"}]`),
		DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: "projection_revision_new_input",
	})
	require.NoError(t, err)
	_, found := admitNextAgentInputAndOpenTurnForTest(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, fixture.Lock.ID,
	)
	require.True(t, found)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.Equal(t, checkpoint.ID, state.RecoveryCheckpointID)
	require.Equal(t, new(0), state.RecoveryCheckpointRetainedBytes)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, current, 128))
	assertPgErrorMessage(t, err, "23514", "recovery checkpoint retained bytes must decrease")
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: current.ID, ErrorKind: modelprotocol.ErrorKindTransient, ErrorMessage: "change model revision",
		})
	require.NoError(t, err)
	revision, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(
		ctx, testOrgID, current.ConfiguredModelRevisionID,
	)
	require.NoError(t, err)
	updated, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: testOrgID, ModelProviderConfigID: revision.ModelProviderConfigID,
		ID: revision.ConfiguredModelID, ContextWindowTokens: new(revision.ContextWindowTokens + 1),
	})
	require.NoError(t, err)
	next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: current.ID,
	})
	require.NoError(t, err)
	require.True(t, next.Claimed)
	require.Equal(t, updated.CurrentRevisionID, next.Context.ConfiguredModelRevisionID)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(
		ctx, testProjectID, fixture.AgentID, next.Context.ID,
	)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, state.RecoveryCheckpointID)
	require.Nil(t, state.RecoveryCheckpointRetainedBytes)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx, checkpointProjectionFailure(fixture, next.Context, 512),
	)
	require.NoError(t, err)
}

func TestFailedCheckpointReplacementResumesNormalDurably(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_failure", true: "runtime_release"}[interrupted], func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fixture, checkpoint, current := checkpointProjectionFixture(t, "replacement_resume_"+t.Name())
			handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
				ctx, executionstore.RecordModelCallFailureAndClaimCompactionInput{
					ParentContextID: current.ID, ReplacesCheckpointID: checkpoint.ID,
					SourceEventSequenceEnd: checkpoint.SummarizedThroughEventSequence,
					Failure: executionstore.RecordRecoverableModelCallFailureInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: current.ID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
						ErrorKind: modelprotocol.ErrorKindContextWindow, ErrorMessage: "checkpoint too large",
					},
				})
			require.NoError(t, err)
			if interrupted {
				replacement, err := fixture.Store.Execution().ReplaceCompactionSource(ctx,
					executionstore.ReplaceCompactionSourceInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
						ModelCallContextID: handoff.CompactionCall.Context.ID,
						ErrorKind:          modelprotocol.ErrorKindContextWindow, ErrorMessage: "summary source exceeds context",
						NextSourceEventSequenceEnd: checkpoint.SummarizedThroughEventSequence,
						NextSourceExcerptBytes:     new(512),
					})
				require.NoError(t, err)
				child := replacement.CompactionCall.Context
				require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
				))
				failed, found, err := fixture.Store.Execution().GetModelCallContext(
					ctx, testProjectID, fixture.AgentID, child.ID,
				)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.ModelCallRecoveryRetry, failed.RecoveryKind)
				require.Empty(t, failed.OptionalCompactionOutcome)
				work, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, child.ID, work.ModelCallContextID)
				fixture.Lock, err = fixture.Store.Execution().AcquireAgentRuntimeLock(
					ctx, testProjectID, fixture.AgentID, testWorkerProcessID, time.Minute,
				)
				require.NoError(t, err)
				for retries := 1; retries <= executionstore.MaxModelCallRetriesPerOperation; retries++ {
					claim, err := fixture.Store.Execution().ClaimNextModelCallContext(
						ctx, executionstore.ClaimNextModelCallContextInput{
							ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
							PredecessorModelCallContextID: child.ID,
						})
					require.NoError(t, err)
					require.True(t, claim.Claimed)
					child = claim.Context
					require.Equal(t, checkpoint.ID, child.ReplacesCheckpointID)
					require.Equal(t, current.ID, child.ParentNormalModelCallContextID)
					require.Equal(t, new(512), child.SourceExcerptBytes)
					state, err := fixture.Store.Execution().GetModelCallRecoveryState(
						ctx, testProjectID, fixture.AgentID, child.ID,
					)
					require.NoError(t, err)
					require.True(t, state.CheckpointRecompressionAttempted)
					require.Equal(t, retries, state.RetryCount)
					if retries < executionstore.MaxModelCallRetriesPerOperation {
						_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
							ctx, executionstore.RecordRecoverableModelCallFailureInput{
								ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
								ModelCallContextID: child.ID, ErrorKind: modelprotocol.ErrorKindProviderUnavailable,
								ErrorMessage: "provider temporarily unavailable",
							})
						require.NoError(t, err)
					}
				}
				require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
				))
				fixture.Lock, err = fixture.Store.Execution().AcquireAgentRuntimeLock(
					ctx, testProjectID, fixture.AgentID, testWorkerProcessID, time.Minute,
				)
				require.NoError(t, err)
			} else {
				failure := executionstore.RecordCompactionFailureAndResumeNormalInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
					ModelCallContextID: handoff.CompactionCall.Context.ID,
					ErrorKind:          modelprotocol.ErrorKindTransient, ErrorMessage: "summary cannot be produced",
				}
				failure.Outcome = executionstore.OptionalCompactionInterrupted
				_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
				failure.Outcome = ""
				_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
				require.NoError(t, err)
				_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
				require.NoError(t, err)
				failure.ErrorMessage = "changed evidence"
				_, err = fixture.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx, failure)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			}
			work, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, current.ID, work.ModelCallContextID)
			next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				PredecessorModelCallContextID: current.ID,
			})
			require.NoError(t, err)
			require.True(t, next.Claimed)
			state, err := fixture.Store.Execution().GetModelCallRecoveryState(
				ctx, testProjectID, fixture.AgentID, next.Context.ID,
			)
			require.NoError(t, err)
			require.True(t, state.CheckpointRecompressionAttempted)
			_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
				ctx, checkpointProjectionFailure(fixture, next.Context, 512),
			)
			require.NoError(t, err)
		})
	}
}

func TestCheckpointProjectionGuardsRejectInvalidOrCurrentHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, checkpoint, current := checkpointProjectionFixture(t, "projection_guards")
	for _, tc := range []struct {
		retained *int
		kind     string
		format   string
	}{
		{new(-1), "context_window", "openai_responses"},
		{new(128), "transient", "openai_responses"},
		{new(128), "context_window", ""},
		{new(len(checkpoint.Summary)), "context_window", "openai_responses"},
	} {
		_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts
SET state='failed',recovery_kind='retry',retry_at=statement_timestamp(),
error_kind=$3,error_message='projection guard',api_format=$4,api_variant='default',
recovery_checkpoint_retained_bytes=$2,completed_at=statement_timestamp()
WHERE id=$1`, current.ID, tc.retained, tc.kind, tc.format)
		require.Error(t, err)
	}
	_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, current, 128))
	require.NoError(t, err)

	protected, _, opening := newStartedNormalModelCallTestFixture(t, ctx, "projection_protected")
	_, err = protected.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(protected, opening.Context, 128))
	assertPgErrorMessage(t, err, "23514", "recovery projection must reduce the latest applicable checkpoint")
	handoff := admissionHandoff(t, ctx, protected, opening, false, opening.Context.InputEventSequence)
	_, err = protected.Store.Execution().RecordCompactionFailureAndResumeNormal(ctx,
		executionstore.RecordCompactionFailureAndResumeNormalInput{
			ProjectID: testProjectID, AgentID: protected.AgentID, RuntimeLockID: protected.Lock.ID,
			ModelCallContextID: handoff.CompactionCall.Context.ID,
			ErrorKind:          modelprotocol.ErrorKindTransient,
			ErrorMessage:       "advancing summary must retain required recovery",
		})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = publishCheckpointForRangeTest(
		t, ctx, protected, handoff.CompactionCall, strings.Repeat("protected opening ", 256), protected.Now,
	)
	require.NoError(t, err)
	next := insertProjectionNormalContext(t, protected, opening.Context.ID, uuid.Nil, uuid.Nil)
	_, err = protected.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(protected, next, 128))
	assertPgErrorMessage(t, err, "23514", "recovery projection must reduce the latest applicable checkpoint")
}

func TestCheckpointProjectionResetsWhenAgentConfigurationChanges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, _, current := checkpointProjectionFixture(t, "projection_config")
	_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, current, 0))
	require.NoError(t, err)
	config := mustCreateAgentConfigFromYAML(t, ctx, fixture.Store,
		strings.Replace(testAgentConfigYAML(), "instruction: test", "instruction: changed setup", 1))
	require.NotEqual(t, current.AgentConfigID, config.ID)
	next := insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, config.ID)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, next.ID)
	require.NoError(t, err)
	require.Nil(t, state.RecoveryCheckpointRetainedBytes)
	require.Equal(t, uuid.Nil, state.RecoveryCheckpointID)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, next, 512))
	require.NoError(t, err)
}

func TestCheckpointProjectionResetsWhenCheckpointAdvances(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, checkpoint, current := checkpointProjectionFixture(t, "projection_new_checkpoint")
	_, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, current, 0))
	require.NoError(t, err)
	current = insertProjectionNormalContext(t, fixture, current.ID, uuid.Nil, uuid.Nil)
	_, output := createModelOutputEventForTurnTest(t, ctx, fixture, uuid.Nil, current.ID,
		"projection_new_checkpoint_done", "end_turn", "", fixture.Now)
	claim := claimSentCompactionForRangeTest(t, ctx, fixture,
		checkpoint.SummarizedThroughEventSequence+1, output.Event.Sequence, output.Event.Sequence, fixture.Now)
	updated, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, strings.Repeat("new summary ", 100), fixture.Now)
	require.NoError(t, err)
	pinned, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, current.ID)
	require.NoError(t, err)
	require.Equal(t, checkpoint.ID, pinned.RecoveryCheckpointID)
	require.Equal(t, new(0), pinned.RecoveryCheckpointRetainedBytes)
	next := insertProjectionNormalContext(t, fixture, claim.Context.ID, uuid.Nil, uuid.Nil)
	state, err := fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, next.ID)
	require.NoError(t, err)
	require.Nil(t, state.RecoveryCheckpointRetainedBytes)
	require.Equal(t, uuid.Nil, state.RecoveryCheckpointID)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx,
		checkpointProjectionFailure(fixture, next, 512))
	require.NoError(t, err)
	retry, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: next.ID,
	})
	require.NoError(t, err)
	require.True(t, retry.Claimed)
	state, err = fixture.Store.Execution().GetModelCallRecoveryState(ctx, testProjectID, fixture.AgentID, retry.Context.ID)
	require.NoError(t, err)
	require.Equal(t, updated.ID, state.RecoveryCheckpointID)
	require.Equal(t, new(512), state.RecoveryCheckpointRetainedBytes)
}
