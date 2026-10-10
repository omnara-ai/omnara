//go:build integration

package agentexecution_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestExecutionAdvanceBudgetsAndRecovery(t *testing.T) {
	for _, mode := range []string{"tools",
		"model_budget",
		"input_budget",
		"retained",
		"model",
		"canceled",
		"expired"} {
		t.Run(mode, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			ref := toolCommand(t, h, lease, "built_in")
			_, err := h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			if mode == "retained" {
				_, err = h.RunTool(t.Context(), ref)
				require.NoError(t, err)
			}
			if mode == "model_budget" || mode == "input_budget" || mode == "model" {
				_, err = h.CompleteTool(
					t.Context(),
					agentexecution.ToolCompletion{
						ToolRef: ref,
						Outcome: "succeeded",
						Content: []agentexecution.Content{{Kind: "text", Text: "done"}},
					},
				)
				require.NoError(t, err)
			}
			if mode == "input_budget" {
				receiveCommand(t, h, "steering", "new input")
			}
			if mode == "canceled" || mode == "expired" {
				query := `UPDATE agent_runtime_locks SET cancel_requested_at=statement_timestamp() WHERE id=$1`
				if mode == "expired" {
					query = `UPDATE agent_runtime_locks SET started_at=statement_timestamp()-interval '3 minutes',
 renewed_at=statement_timestamp()-interval '2 minutes',
 lease_expires_at=statement_timestamp()-interval '1 minute' WHERE id=$1`
				}
				_, err = u.DB().Exec(t.Context(), query, lease)
				require.NoError(t, err)
				require.ErrorIs(
					t,
					u.Savepoint(
						t.Context(),
						func(_ *agentexecution.Unit) error { _, err := h.Advance(t.Context(), lease, true, true); return err },
					),
					storeerr.ErrRuntimeLockInactive,
				)
				require.NoError(t, u.Commit(t.Context(), "fenced advance"))
				return
			}
			work, err := h.Advance(t.Context(), lease, mode == "model", true)
			require.NoError(t, err)
			switch mode {
			case "tools":
				require.Equal(t, agentexecution.WorkTool, work.Selection.Work)
				require.False(t, work.Released)
			case "model":
				require.Equal(t, agentexecution.WorkModel, work.Selection.Work)
				require.NotEqual(t, uuid.Nil, work.Model.Context.ID)
			default:
				require.True(t, work.Released)
			}
			snapshot := assertCommandState(t, u, h)
			if mode == "input_budget" {
				require.Equal(t, agentexecution.AdmitAllSteering, snapshot.Selection.Admission)
			}
			if mode == "retained" {
				require.Equal(t, agentexecution.WorkModel, snapshot.Selection.Work)
			}
			require.NoError(t, u.Commit(t.Context(), "advance"))
		})
	}
}

func TestExecutionClaimRenewAcceptAdvance(t *testing.T) {
	f, a, oldLease := commandFixture(t)
	_, err := f.pool.Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE id=$1`, oldLease)
	require.NoError(t, err)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "claim")
	claimed, err := h.Claim(t.Context(), uuid.New(), 90*time.Second)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, claimed.RuntimeID)
	require.ErrorIs(
		t,
		u.Savepoint(
			t.Context(),
			func(_ *agentexecution.Unit) error {
				_, err := h.Claim(t.Context(), uuid.New(), 90*time.Second)
				return err
			},
		),
		storeerr.ErrAgentNotAdvanceable,
	)
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
	accepted, work, err := h.AcceptAndAdvance(t.Context(), input, true, true)
	require.NoError(t, err)
	require.True(t, accepted.Created)
	require.NotEqual(t, prepared.Context.ID, work.Model.Context.ID)
	replay, work, err := h.AcceptAndAdvance(t.Context(), input, true, true)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, uuid.Nil, work.Model.Context.ID)
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "claim renew advance"))
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
						Failure: agentexecution.ModelFailure{ContextID: prepared.Context.ID,
							RuntimeLockID: lease,
							ErrorKind:     "context_length",
							ErrorMessage:  "compact"}})
				require.NoError(t, err)
				prepared = start.Model
			}
			for range 8 {
				_,
					err = h.FailModel(t.Context(),
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
		})
	}
}

func TestExecutionRenewalSamplesTimeAfterLock(t *testing.T) {
	f, a, lease := commandFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE agent_runtime_locks SET
 lease_expires_at=statement_timestamp()+interval '2 seconds' WHERE id=$1`, lease)
	require.NoError(t, err)
	blocker, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = blocker.Exec(t.Context(), `SELECT id FROM agent_runtime_locks WHERE id=$1 FOR UPDATE`, lease)
	require.NoError(t, err)
	cell := agentexecution.NewCell("test", f.pool, nil, nil)
	result := make(chan error, 1)
	go func() {
		u, err := cell.Begin(t.Context())
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = u.Rollback(context.Background()) }()
		_,
			err = u.RenewRuntime(t.Context(),
			agentexecution.AgentRoute{CellID: "test",
				ProjectID:   f.project,
				AgentID:     a,
				RootAgentID: a},
			lease,
			90*time.Second)
		result <- err
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0)`).Scan(&blocked)
		return err == nil && blocked
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var expired bool
		err := f.pool.QueryRow(t.Context(), `SELECT lease_expires_at<=statement_timestamp()
 FROM agent_runtime_locks WHERE id=$1`, lease).Scan(&expired)
		return err == nil && expired
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, blocker.Commit(t.Context()))
	require.NoError(t, <-result)
}
