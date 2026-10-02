//go:build integration

package executionstore_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestCheckpointRecompressionPreservesCoverageLineageAndOmissionHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_recompression")
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	firstClaim := claimSentCompactionForRangeTest(t, ctx, fixture, 1, frontier, frontier, fixture.Now)
	first, err := publishCheckpointForRangeTest(
		t, ctx, fixture, firstClaim, strings.Repeat("a", 1000), fixture.Now.Add(time.Second),
	)
	require.NoError(t, err)
	require.False(t, first.HasOmittedHistory)

	claimReplacement := func(prior executionstore.ContextCheckpointRecord) executionstore.ModelCallClaim {
		t.Helper()
		var parentID uuid.UUID
		require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
    org_id, project_id, agent_id, operation_kind, attempt_number, agent_config_id,
    configured_model_revision_id, input_event_sequence, runtime_lock_id, state, created_at
)
SELECT org_id, project_id, agent_id, 'normal', 1, agent_config_id,
       configured_model_revision_id, $2, runtime_lock_id, 'started', statement_timestamp()
FROM model_call_contexts WHERE id=$1 RETURNING id`,
			prior.ProducerModelCallContextID, prior.CheckpointEventSequence,
		).Scan(&parentID))
		input := executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parentID, ReplacesCheckpointID: prior.ID,
			SourceEventSequenceEnd: prior.SummarizedThroughEventSequence,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
				ModelCallContextID: parentID, RecoveryKind: executionstore.ModelCallRecoveryCompact,
				ErrorKind: modelprotocol.ErrorKindContextWindow, ErrorMessage: "prior checkpoint exceeds context",
			},
		}
		if prior.ID != first.ID {
			input.ReplacesCheckpointID = first.ID
			_, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			input.ReplacesCheckpointID = prior.ID
		}
		handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(ctx, input)
		require.NoError(t, err)
		require.Equal(t, prior.ID, handoff.CompactionCall.Context.ReplacesCheckpointID)
		return handoff.CompactionCall
	}
	claim := claimReplacement(first)
	_, err = fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx, executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindProviderUnavailable,
			ErrorMessage: "temporary summary failure",
		},
	)
	require.NoError(t, err)
	claim, err = fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		PredecessorModelCallContextID: claim.Context.ID,
	})
	require.NoError(t, err)
	require.True(t, claim.Claimed)
	require.Equal(t, first.ID, claim.Context.ReplacesCheckpointID)
	_, err = fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts SET replaces_checkpoint_id=NULL WHERE id=$1`,
		claim.Context.ID)
	require.ErrorContains(t, err, "immutable")
	bytes := 64
	updated, err := fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindContextWindow,
		ErrorMessage: "full prior summary cannot fit", NextSourceEventSequenceEnd: first.SummarizedThroughEventSequence,
		NextSourceExcerptBytes: &bytes,
	})
	require.NoError(t, err)
	claim = updated.CompactionCall
	require.Equal(t, first.ID, claim.Context.ReplacesCheckpointID)
	require.Equal(t, &bytes, claim.Context.SourceExcerptBytes)
	_, err = publishCheckpointForRangeTest(t, ctx, fixture, claim, strings.Repeat("b", 901), fixture.Now)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	second, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, strings.Repeat("b", 900), fixture.Now)
	require.NoError(t, err)
	require.Equal(t, first.SummarizedThroughEventSequence, second.SummarizedThroughEventSequence)
	require.True(t, second.HasOmittedHistory)
	require.Greater(t, second.CheckpointEventSequence, first.CheckpointEventSequence)

	claim = claimReplacement(second)
	require.Nil(t, claim.Context.SourceExcerptBytes)
	third, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, strings.Repeat("c", 810), fixture.Now)
	require.NoError(t, err)
	require.True(t, third.HasOmittedHistory)
	latest, found, err := fixture.Store.Execution().GetLatestApplicableContextCheckpoint(
		ctx, testProjectID, fixture.AgentID, third.CheckpointEventSequence,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, third.ID, latest.ID)
	require.True(t, latest.HasOmittedHistory)
	storedFirst, found, err := fixture.Store.Execution().GetContextCheckpoint(
		ctx, testProjectID, fixture.AgentID, first.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, storedFirst.HasOmittedHistory)
	require.Equal(t, first.Summary, storedFirst.Summary)
}
