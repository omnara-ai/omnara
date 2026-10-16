//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestReadyAgentBacklogUsesClaimableTime(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "ready_backlog")
	execution := f.Store.Execution()
	ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "queued")

	backlog, err := execution.SampleReadyAgentBacklog(ctx)
	require.NoError(t, err)
	require.Equal(t, executionstore.ReadyAgentBacklog{}, backlog, "locked agents are not claimable")

	require.NoError(t, execution.ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE agent_wakeups SET ready_at = ready_at - interval '1 hour' WHERE agent_id = $1`, f.AgentID)
	require.NoError(t, err)
	backlog, err = execution.SampleReadyAgentBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, backlog.Ready)
	require.Less(t, backlog.OldestReadyAge, time.Minute, "backdated ready_at counts from when the wakeup was published")

	_, err = f.Store.pool.Exec(ctx,
		`UPDATE agent_wakeups SET updated_at = updated_at - interval '10 minutes' WHERE agent_id = $1`, f.AgentID)
	require.NoError(t, err)
	backlog, err = execution.SampleReadyAgentBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, backlog.Ready)
	require.GreaterOrEqual(t, backlog.OldestReadyAge, 10*time.Minute)
	require.Less(t, backlog.OldestReadyAge, 11*time.Minute)

	work, found, err := execution.ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, f.AgentID, work.AgentID)

	backlog, err = execution.SampleReadyAgentBacklog(ctx)
	require.NoError(t, err)
	require.Equal(t, executionstore.ReadyAgentBacklog{}, backlog)
}
