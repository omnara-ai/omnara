//go:build integration

package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestExecutionClaimRenewAcceptRelease(t *testing.T) {
	f, a, oldLease := commandFixture(t)
	_, err := f.pool.Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE id=$1`, oldLease)
	require.NoError(t, err)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "claim")
	claimed, err := h.Claim(t.Context(), uuid.New(), 90*time.Second)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, claimed.RuntimeID)
	require.NoError(t, u.Commit(t.Context(), "before rejected command"))
	u, h = f.handle(t, a)
	_, err = h.Claim(t.Context(), uuid.New(), 90*time.Second)
	require.ErrorIs(t, err, storeerr.ErrAgentNotAdvanceable)
	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, a)
	route, err := h.Route()
	require.NoError(t, err)
	_, err = u.RenewRuntime(t.Context(), route, claimed.RuntimeID, 90*time.Second)
	require.NoError(t, err)
	prepared := prepareCommand(t, h, claimed.RuntimeID)
	input := agentexecution.AcceptOutputInput{
		RuntimeLockID: claimed.RuntimeID,
		ContextID:     prepared.Context.ID,
		Response:      commandResponse(modelenvelope.StopReasonMaxTokens),
	}
	accepted, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, accepted.Created)
	replay, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.NoError(t, h.Release(t.Context(), input.RuntimeLockID))
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "claim renew accept release"))
}

func TestExecutionRecoveryExhaustion(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "compaction"}[compact], func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "exhaustion")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			prepared := prepareCommand(t, h, lease)
			if compact {
				start,
					err := h.BeginCompaction(t.Context(),
					agentexecution.BeginCompactionInput{SourceEnd: prepared.Context.InputEventSequence,
						Failure: agentexecution.ModelFailure{Recovery: agentexecution.RecoveryCompact, ContextID: prepared.Context.ID,
							RuntimeLockID: lease,
							ErrorKind:     "context_length",
							ErrorMessage:  "compact"}})
				require.NoError(t, err)
				prepared = start.Model
			}
			for range 8 {
				_, err = h.FailModel(t.Context(),
					agentexecution.ModelFailure{ContextID: prepared.Context.ID,
						RuntimeLockID: lease,
						Recovery:      agentexecution.RecoveryRetry,
						ErrorKind:     "transient",
						ErrorMessage:  "retry"})
				require.NoError(t, err)
				prepared = prepareCommand(t, h, lease)
			}
			require.EqualValues(t, 9, prepared.Context.Attempt)
			require.NoError(t, h.Release(t.Context(), lease))
			snapshot := assertCommandState(t, u, h)
			require.False(t, snapshot.Selection.TurnContinuable)
			require.Equal(t, agentexecution.WorkNone, snapshot.Selection.Work)
			var terminal int
			require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT count(*) FROM model_call_contexts
 WHERE id=$1 AND state='failed' AND recovery_kind IS NULL AND error_kind='runtime'
 AND error_code='runtime_released_before_model_result_acceptance'`, prepared.Context.ID).Scan(&terminal))
			require.Equal(t, 1, terminal)
			var outputs int
			require.NoError(t,
				u.DB().QueryRow(t.Context(),
					`SELECT count(*) FROM model_outputs WHERE agent_id=$1 AND stop_reason='error'`,
					a).Scan(&outputs))
			require.Equal(t, 1, outputs)
			reaped, err := h.Reap(t.Context(), lease)
			require.NoError(t, err)
			require.False(t, reaped)
			require.NoError(t, u.Commit(t.Context(), "recovery exhausted"))
			var wakeups int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&wakeups))
			require.Zero(t, wakeups)
		})
	}
}

func TestExecutionClaimInvalidLease(t *testing.T) {
	for _, duration := range []time.Duration{time.Second, time.Hour} {
		t.Run(duration.String(), func(t *testing.T) {
			f, a, _ := commandFixture(t)
			_, h := f.handle(t, a)
			_, err := h.Claim(t.Context(), uuid.New(), duration)
			require.ErrorContains(t, err, "runtime lease duration must be between 15s and 30m0s")
		})
	}
}

func TestExecutionCompactionExcerptRecoveryUsesRetryCount(t *testing.T) {
	f, agent, lease := commandFixture(t)
	unit, handle := f.handle(t, agent)
	receiveCommand(t, handle, "steering", "first")
	receiveCommand(t, handle, "steering", "second")
	_, err := handle.AdmitInputs(t.Context())
	require.NoError(t, err)
	normal := prepareCommand(t, handle, lease)
	failure := agentexecution.ModelFailure{
		ContextID: normal.Context.ID, RuntimeLockID: lease, Recovery: agentexecution.RecoveryCompact,
		ErrorKind: modelprotocol.ErrorKindContextWindow, ErrorMessage: "provider overflow",
		Evidence: agentexecution.ModelEvidence{APIFormat: "openai-responses", APIVariant: "default"},
	}
	compact, err := handle.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{
		Failure: failure, SourceEnd: normal.Context.InputEventSequence,
	})
	require.NoError(t, err)
	for range executionpolicy.MaxModelCallRetriesPerOperation + 1 {
		failure.ContextID = compact.Model.Context.ID
		failure.Recovery = agentexecution.RecoveryReduce
		compact, err = handle.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{
			Failure: failure, SourceEnd: compact.Model.Context.SourceEventSequenceEnd,
		})
		require.NoError(t, err)
		require.True(t, compact.Model.Created)
		assertCommandState(t, unit, handle)
	}
	require.Greater(t, int(compact.Model.Context.Attempt), executionpolicy.MaxModelCallRetriesPerOperation)
	_, err = handle.FailModel(t.Context(), agentexecution.ModelFailure{
		ContextID: compact.Model.Context.ID, RuntimeLockID: lease, Recovery: agentexecution.RecoveryRetry,
		ErrorKind: modelprotocol.ErrorKindProviderUnavailable, ErrorMessage: "transient",
	})
	require.NoError(t, err)
	compact.Model = prepareCommand(t, handle, lease)
	require.NoError(t, unit.Commit(t.Context(), "excerpt attempts and retry"))
	unit, handle = f.handle(t, agent)
	failure.ContextID = compact.Model.Context.ID
	_, err = handle.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{
		Failure: failure, SourceEnd: compact.Model.Context.SourceEventSequenceEnd - 1,
	})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.NoError(t, unit.Rollback(t.Context()))
	unit, handle = f.handle(t, agent)
	require.NoError(t, handle.Release(t.Context(), lease))
	var recovery string
	var delay int64
	require.NoError(t, unit.DB().QueryRow(t.Context(), `
SELECT recovery_kind,extract(epoch FROM (retry_at-completed_at))*1000000
 FROM model_call_contexts WHERE id=$1`, compact.Model.Context.ID).Scan(&recovery, &delay))
	require.Equal(t, "retry", recovery)
	require.Equal(t, modelprotocol.RetryBackoff(2, compact.Model.Context.ID.String()).Microseconds(), delay)
	snapshot := assertCommandState(t, unit, handle)
	require.Equal(t, agentexecution.OriginRetry, snapshot.Selection.Model.Origin)
	require.NoError(t, unit.Commit(t.Context(), "recover second transient failure"))
}
