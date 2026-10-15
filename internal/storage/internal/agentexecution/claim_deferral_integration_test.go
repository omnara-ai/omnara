//go:build integration

package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestDeferClaimPreservesChangedWakeup(t *testing.T) {
	ctx := t.Context()
	f := migratedExecutionFixture(t)
	agent := f.create(t, uuid.New(), uuid.Nil)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	_, err := f.pool.Exec(ctx, `INSERT INTO agent_wakeups(agent_id,ready_at,updated_at)
VALUES($1,statement_timestamp()+interval '5 minutes',statement_timestamp())`, agent)
	require.NoError(t, err)
	var before, after time.Time
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, agent).Scan(&before))
	require.NoError(t, cell.DeferClaim(ctx, agent))
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, agent).Scan(&after))
	require.Equal(t, before, after)
	_, err = f.pool.Exec(ctx, `DELETE FROM agent_wakeups WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	require.NoError(t, cell.DeferClaim(ctx, agent))
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups WHERE agent_id=$1`, agent).Scan(&count))
	require.Zero(t, count)
}
