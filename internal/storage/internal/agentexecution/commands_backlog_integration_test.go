//go:build integration

package agentexecution_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	u, h := f.handle(t, a)
	rollback := errors.New("restore backlog")
	for _, dense := range []bool{false, true} {
		for _, position := range []string{"front", "back", "before", "after"} {
			name := position
			if dense {
				name += "/rebalance"
			}
			t.Run(name, func(t *testing.T) {
				err := u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
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
					return rollback
				})
				require.ErrorIs(t, err, rollback)
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
	promote := agentexecution.BacklogChange{ID: second.ID, DeliveryMode: "steering"}
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
		agentexecution.BacklogChange{ID: third.ID, DeliveryMode: "queued"},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assertCommandState(t, u, h)
	err = u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
		_, err := h.ChangeBacklog(
			t.Context(),
			agentexecution.BacklogChange{ID: third.ID, DeliveryMode: "queued"},
		)
		return err
	})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	_, err = h.AdmitInputs(t.Context())
	require.NoError(t, err)
	changed, err = h.ChangeBacklog(t.Context(), promote)
	require.NoError(t, err)
	require.False(t, changed)
	assertCommandState(t, u, h)
	cancel := agentexecution.BacklogChange{ID: first.ID, DeliveryMode: "queued", Cancel: true}
	changed, err = h.ChangeBacklog(t.Context(), cancel)
	require.NoError(t, err)
	require.True(t, changed)
	err = u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
		_, err := h.ChangeBacklog(t.Context(), cancel)
		return err
	})
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	assertCommandState(t, u, h)
}
