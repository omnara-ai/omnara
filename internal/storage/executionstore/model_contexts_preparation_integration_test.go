//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPrepareNormalModelCallUsesSnapshotAfterLockWait(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepare_snapshot")
	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	blocker, err := f.Store.Execution().IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	_, err = agentexecution.LockTestAgent(ctx, blocker, testProjectID, f.AgentID, nil)
	require.NoError(t, err)
	var pid int32
	require.NoError(t, blocker.DB().QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	type result struct {
		prepared executionstore.PreparedNormalModelCall
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := f.Store.Execution().
			PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
				ProjectID:       testProjectID,
				AgentID:         f.AgentID,
				RuntimeLockID:   f.Lock.ID,
				OpeningInputIDs: []uuid.UUID{input.ID},
			})
		done <- result{prepared, err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "", pid)
	admitted, err := executionstore.IntegrationAdmitInputs(ctx, blocker, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Len(t, admitted.Events, 1)
	sequence := admitted.Events[0].Sequence
	require.NoError(t, blocker.Commit(ctx, "admit after wait"))
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.prepared.Claim.Claimed)
	require.Equal(t, sequence, got.prepared.Snapshot.InputEventSequence)
	require.Equal(t, sequence, got.prepared.Claim.Context.InputEventSequence)
	require.Equal(t, got.prepared.Snapshot.AgentConfig.ID, got.prepared.Claim.Context.AgentConfigID)
}

func TestPrepareNormalModelCallRollbackAndReplay(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepare_rollback")
	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	reclaimAgentWork(t, ctx, &f)
	prepare := executionstore.PrepareNormalModelCallInput{
		ProjectID:       testProjectID,
		AgentID:         f.AgentID,
		RuntimeLockID:   f.Lock.ID,
		OpeningInputIDs: []uuid.UUID{input.ID},
	}
	_, err := f.Store.pool.Exec(
		ctx,
		`CREATE FUNCTION reject_prepared_context() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'reject'; END $$;
CREATE CONSTRAINT TRIGGER reject_prepared_context AFTER INSERT ON model_call_contexts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_prepared_context();`,
	)
	require.NoError(t, err)
	_, err = f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.ErrorContains(t, err, "commit prepare model call")
	var count int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1`, f.AgentID,
	).Scan(&count))
	require.Zero(t, count)
	_, err = f.Store.pool.Exec(ctx, `DROP TRIGGER reject_prepared_context ON model_call_contexts`)
	require.NoError(t, err)
	prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	require.Equal(t, prepared.Snapshot.AgentConfig.ID, prepared.Claim.Context.AgentConfigID)
	require.Equal(t, prepared.Snapshot.InputEventSequence, prepared.Claim.Context.InputEventSequence)
	replay, err := f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.NoError(t, err)
	require.False(t, replay.Claim.Created)
	require.False(t, replay.Claim.Claimed)
	require.Equal(t, prepared.Claim.Context.ID, replay.Claim.Context.ID)
}

func TestPrepareNormalModelCallValidatesOwnershipAndSelectedWork(t *testing.T) {
	for _, mode := range []string{
		"outside opening", "subset", "duplicate", "opening inputs", "missing source", "wrong source", "wrong context",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "prepare_validation")
			initial := mode == "outside opening" || mode == "subset" || mode == "duplicate"
			var work executionstore.ClaimedAgentWork
			if initial {
				first := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "first")
				second := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "second")
				work = reclaimAgentWork(t, ctx, &f)
				require.Equal(t, []uuid.UUID{first.ID, second.ID}, work.Model.InputIDs)
				admitted := work.Model.AdmittedInputTurn
				require.Len(t, admitted.Events, 2)
				turns, err := f.Store.Execution().ListAgentTurnsForRead(ctx, testProjectID, f.AgentID, 0, 1)
				require.NoError(t, err)
				require.Len(t, turns, 1)
				require.Equal(t, admitted.Turn.ID, turns[0].ID)
				require.True(t, turns[0].StartedAt.Equal(admitted.Events[0].At))
				require.True(t, turns[0].UpdatedAt.Equal(admitted.Events[1].At))
			} else {
				id := createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
				ownedCompleteTool(t, ctx, f, id)
				work = reclaimAgentWork(t, ctx, &f)
			}
			input := executionstore.PrepareNormalModelCallInput{
				ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
				OpeningInputIDs:          work.Model.InputIDs,
				SourceModelCallContextID: work.Model.SourceModelCallContextID,
				SourceModelOutputID:      work.Model.SourceModelOutputID,
			}
			invalid := input
			switch mode {
			case "outside opening":
				var configInput uuid.UUID
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT id FROM agent_inputs WHERE agent_id=$1 AND input_kind='config_change' LIMIT 1`, f.AgentID).Scan(&configInput))
				invalid.OpeningInputIDs = []uuid.UUID{configInput}
			case "subset":
				invalid.OpeningInputIDs = input.OpeningInputIDs[:1]
			case "duplicate":
				invalid.OpeningInputIDs = []uuid.UUID{input.OpeningInputIDs[0], input.OpeningInputIDs[0]}
			case "opening inputs":
				invalid.OpeningInputIDs = []uuid.UUID{uuid.New()}
			case "missing source":
				invalid.SourceModelCallContextID, invalid.SourceModelOutputID = uuid.Nil, uuid.Nil
			case "wrong source":
				invalid.SourceModelOutputID = uuid.New()
			case "wrong context":
				invalid.SourceModelCallContextID = uuid.New()
			}
			_, err := f.Store.Execution().PrepareNormalModelCall(ctx, invalid)
			require.ErrorIs(t, err, storeerr.ErrAgentNotAdvanceable)
			var count int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND state='started'`, f.AgentID).Scan(&count))
			require.Zero(t, count)
			watermark, err := f.Store.Execution().MaxEventSequence(ctx, testProjectID, f.AgentID)
			require.NoError(t, err)
			if initial {
				require.Equal(t, work.Model.AdmittedInputTurn.Events[1].Sequence, watermark)
			}
			prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, input)
			require.NoError(t, err)
			require.True(t, prepared.Claim.Created)
			require.True(t, prepared.Claim.Claimed)
			require.Equal(t, watermark, prepared.Claim.Context.InputEventSequence)
			replay, err := f.Store.Execution().PrepareNormalModelCall(ctx, input)
			require.NoError(t, err)
			require.Equal(t, prepared.Claim.Context.ID, replay.Claim.Context.ID)
			require.False(t, replay.Claim.Created)
			require.False(t, replay.Claim.Claimed)
		})
	}
}
