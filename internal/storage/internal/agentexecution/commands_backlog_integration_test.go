//go:build integration

package agentexecution_test

import (
	"fmt"
	"github.com/jackc/pgx/v5"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type backlogRecord struct {
	ID    uuid.UUID
	Mode  string
	Rank  int64
	State string
}

func readCommandBacklog(t *testing.T, u *agentexecution.Unit, a uuid.UUID) []backlogRecord {
	t.Helper()
	rows, err := u.DB().Query(t.Context(),
		"SELECT id,delivery_mode,input_rank,state FROM agent_inputs WHERE agent_id=$1 ORDER BY input_rank,queued_at,id",
		a)
	require.NoError(t, err)
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[backlogRecord])
	require.NoError(t, err)
	return result
}

func TestExecutionBacklogOrderingAndReplay(t *testing.T) {
	f, a, _ := commandFixture(t)
	for _, dense := range []bool{false, true} {
		for _, position := range []string{"front", "back", "before", "after"} {
			name := position
			if dense {
				name += "/rebalance"
			}
			t.Run(name, func(t *testing.T) {
				u, h := f.handle(t, a)
				ids := make([]uuid.UUID, 3)
				for i := range ids {
					ids[i] = receiveCommand(t, h, "queued", "input").ID
					if dense {
						_, err := u.DB().Exec(t.Context(),
							"UPDATE agent_inputs SET input_rank=$2 WHERE agent_id=$1 AND id=$3", a, i+1, ids[i])
						require.NoError(t, err)
					}
				}
				change := agentexecution.BacklogChange{
					ID:           ids[2],
					DeliveryMode: "queued",
					Position:     position,
				}
				if position == "before" || position == "after" {
					change.BeforeID = ids[0]
				}
				_, err := h.ChangeBacklog(t.Context(), change)
				require.NoError(t, err)
				expected := readCommandBacklog(t, u, a)
				got := make([]uuid.UUID, len(expected))
				for i, row := range expected {
					got[i] = row.ID
				}
				order := []uuid.UUID{ids[0], ids[1], ids[2]}
				if position == "front" || position == "before" {
					order = []uuid.UUID{ids[2], ids[0], ids[1]}
				}
				if position == "after" {
					order = []uuid.UUID{ids[0], ids[2], ids[1]}
				}
				require.Equal(t, order, got)
				assertCommandState(t, u, h)
				changed, err := h.ChangeBacklog(t.Context(), change)
				require.NoError(t, err)
				require.False(t, changed)
				require.Equal(t, expected, readCommandBacklog(t, u, a))
				require.NoError(t, u.Rollback(t.Context()))
			})
		}
	}
}

func TestExecutionBacklogDeliveryReplay(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	first := receiveCommand(t, h, "queued", "first")
	second := receiveCommand(t, h, "queued", "second")
	third := receiveCommand(t, h, "steering", "third")
	promote := agentexecution.BacklogChange{
		ID: second.ID, DeliveryMode: "steering"}
	changed, err := h.ChangeBacklog(t.Context(), promote)
	require.NoError(t, err)
	require.True(t, changed)
	rows := readCommandBacklog(t, u, a)
	for _, row := range rows {
		if row.ID == second.ID {
			require.EqualValues(t, 2048, row.Rank)
		} else {
			require.EqualValues(t, 1024, row.Rank)
		}
	}
	changed, err = h.ChangeBacklog(t.Context(), promote)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, rows, readCommandBacklog(t, u, a))
	assertCommandState(t, u, h)
	changed, err = h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{
			ID: third.ID, DeliveryMode: "queued"},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "before rejected command"))
	u, h = f.handle(t, a)
	_, err = h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{
			ID: third.ID, DeliveryMode: "queued"},
	)

	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, a)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = h.AdmitInputs(t.Context())
	require.NoError(t, err)
	changed, err = h.ChangeBacklog(t.Context(), promote)
	require.NoError(t, err)
	require.False(t, changed)
	assertCommandState(t, u, h)
	cancel := agentexecution.BacklogChange{
		ID: first.ID, DeliveryMode: "queued", Cancel: true}
	changed, err = h.ChangeBacklog(t.Context(), cancel)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, u.Commit(t.Context(), "before rejected command"))
	u, h = f.handle(t, a)
	_, err = h.ChangeBacklog(t.Context(), cancel)

	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, a)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{ID: first.ID, DeliveryMode: "steering"})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, a)
	assertCommandState(t, u, h)
}

func TestExecutionBacklogRepeatedGapRebalance(t *testing.T) {
	f, a, _ := commandFixture(t)
	other := f.create(t, uuid.New(), uuid.Nil)
	ou, oh := f.handle(t, other)
	receiveCommand(t, oh, "queued", "other queued")
	receiveCommand(t, oh, "steering", "other steering")
	otherBefore := readCommandBacklog(t, ou, other)
	require.NoError(t, ou.Commit(t.Context(), "other backlog"))
	u, h := f.handle(t, a)
	ids := make([]uuid.UUID, 4)
	for i := range ids {
		ids[i] = receiveCommand(t, h, "queued", "queued").ID
	}
	steering := receiveCommand(t, h, "steering", "steering")
	canceled := receiveCommand(t, h, "queued", "canceled")
	_, err := h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
		ID: canceled.ID, DeliveryMode: "queued", Cancel: true})
	require.NoError(t, err)
	var untouched []backlogRecord
	for _, row := range readCommandBacklog(t, u, a) {
		if row.ID == steering.ID || row.ID == canceled.ID {
			untouched = append(untouched, row)
		}
	}
	expected := append([]uuid.UUID(nil), ids...)
	bulk := 0
	for iteration := range 11 {
		selected := ids[2+iteration%2]
		trace, err := agentexecution.TraceExecutionCommand(u, func() error {
			changed, err := h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
				ID: selected, DeliveryMode: "queued", Position: "before", BeforeID: ids[1]})
			if err == nil {
				require.True(t, changed)
			}
			return err
		})
		require.NoError(t, err)
		for _, statement := range trace {
			if statement.Affected > 1 {
				bulk++
				require.EqualValues(t, 4, statement.Affected)
			}
		}
		expected = slices.DeleteFunc(expected, func(id uuid.UUID) bool { return id == selected })
		index := slices.Index(expected, ids[1])
		expected = slices.Insert(expected, index, selected)
		var actual []uuid.UUID
		var rank int64
		var remaining []backlogRecord
		for _, row := range readCommandBacklog(t, u, a) {
			if row.ID == steering.ID || row.ID == canceled.ID {
				remaining = append(remaining, row)
				continue
			}
			actual = append(actual, row.ID)
			require.Greater(t, row.Rank, rank)
			rank = row.Rank
		}
		require.Equal(t, expected, actual)
		require.Equal(t, untouched, remaining)
	}
	require.Equal(t, 1, bulk)
	require.Equal(t, otherBefore, readCommandBacklog(t, u, other))
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "rebalanced backlog"))
}

func TestExecutionBacklogAnchorConcurrentInvalidation(t *testing.T) {
	for _, operation := range []string{"cancel", "admit", "promote"} {
		t.Run(operation, func(t *testing.T) {
			f, a, _ := commandFixture(t)
			u, h := f.handle(t, a)
			anchor := receiveCommand(t, h, "queued", "anchor")
			moved := receiveCommand(t, h, "queued", "moved")
			require.NoError(t, u.Commit(t.Context(), "backlog"))
			u, h = f.handle(t, a)
			waiting, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
			require.NoError(t, err)
			defer func() { _ = waiting.Rollback(t.Context()) }()
			plan, err := waiting.PlanAgents(t.Context(), []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: a}}, nil)
			require.NoError(t, err)
			result := make(chan error, 1)
			go func() {
				err := waiting.LockAgents(t.Context(), plan)
				if err == nil {
					var handle *agentexecution.Handle
					handle, err = waiting.Agent(agentexecution.AgentRoute{ProjectID: f.project, AgentID: a, RootAgentID: a})
					if err == nil {
						_, err = handle.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
							ID: moved.ID, DeliveryMode: "queued", Position: "before", BeforeID: anchor.ID})
					}
				}
				result <- err
			}()
			integrationdb.WaitForLockWaiters(t, t.Context(), f.pool, "FROM agents", 1)
			switch operation {
			case "admit":
				admitted, err := h.AdmitInputs(t.Context())
				require.NoError(t, err)
				require.Len(t, admitted.Inputs, 1)
				require.Equal(t, anchor.ID, admitted.Inputs[0].ID)
			case "cancel":
				_, err = h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
					ID: anchor.ID, DeliveryMode: "queued", Cancel: true})
				require.NoError(t, err)
			case "promote":
				_, err = h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
					ID: anchor.ID, DeliveryMode: "steering"})
				require.NoError(t, err)
			}
			before := readCommandBacklog(t, u, a)
			require.NoError(t, u.Commit(t.Context(), "invalidate anchor"))
			select {
			case err := <-result:
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			case <-time.After(5 * time.Second):
				t.Fatal("move did not finish")
			}
			require.NoError(t, waiting.Rollback(t.Context()))
			check, _ := f.handle(t, a)
			require.Equal(t, before, readCommandBacklog(t, check, a))
		})
	}
}

func TestExecutionAdmissionRemainingInputs(t *testing.T) {
	f, a, _ := commandFixture(t)
	for _, queued := range []int{0, 1, 2} {
		for _, steering := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("queued=%d/steering=%d", queued, steering), func(t *testing.T) {
				u, h := f.handle(t, a)
				for range queued {
					receiveCommand(t, h, "queued", "queued")
				}
				for range steering {
					receiveCommand(t, h, "steering", "steering")
				}
				before, err := h.LoadExecution(t.Context())
				require.NoError(t, err)
				admitted, err := h.AdmitInputs(t.Context())
				require.NoError(t, err)
				expected := steering
				if expected == 0 && queued > 0 {
					expected = 1
				}
				require.Len(t, admitted.Inputs, expected)
				after := assertCommandState(t, u, h)
				if expected == 0 {
					require.Equal(t, before.View, after.View)
				}
				require.False(t, after.View.Inputs.Steering)
				require.Equal(t, queued > 1 || steering > 0 && queued > 0, after.View.Inputs.Queued)
				require.NoError(t, u.Rollback(t.Context()))
			})
		}
	}
}

func TestExecutionConfigInputDoesNotUseBacklogRank(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	for _, mode := range []string{"queued", "steering"} {
		receiveCommand(t, h, mode, "content")
	}
	for range 2 {
		config, err := h.ActivateConfig(t.Context(), agentexecution.ActivateConfigInput{ConfigID: f.config})
		require.NoError(t, err)
		var rank int64
		require.NoError(t, u.DB().QueryRow(t.Context(),
			"SELECT input_rank FROM agent_inputs WHERE agent_id=$1 AND id=$2", a, config.InputID).Scan(&rank))
		require.EqualValues(t, 1024, rank)
		assertCommandState(t, u, h)
	}
	for _, mode := range []string{"queued", "steering"} {
		next := receiveCommand(t, h, mode, "content")
		var rank int64
		require.NoError(t, u.DB().QueryRow(t.Context(),
			"SELECT input_rank FROM agent_inputs WHERE agent_id=$1 AND id=$2", a, next.ID).Scan(&rank))
		require.EqualValues(t, 2048, rank)
	}
}
