//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

// Resolving a question appends both an answer and a tool result. The second
// agent-row update rechecks foreign keys even though the parent ID is unchanged.
// That check must coexist with runtime release holding the parent while waiting
// for this child. Neither public operation retries a deadlock.
func TestChildQuestionResolutionContendsWithRuntimeRelease(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newProcessDaemonFixture(t, ctx, "child_question_release")
	store := fixture.Store
	parent, err := store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	require.NoError(t, err)
	launched, err := spawnSubagentForTest(t, ctx, store, parent, parent.CurrentConfigID,
		"question-child", "question-child", nil, withoutLaunchMessage)
	require.NoError(t, err)
	fixture.AgentID = launched.Agent.ID
	fixture.Lock, err = store.Execution().AcquireAgentRuntimeLock(ctx, testProjectID, fixture.AgentID,
		testWorkerProcessID, time.Minute)
	require.NoError(t, err)
	callID := createToolCallForProcessTest(t, ctx, fixture, "child_question_release", "ask_question")
	interaction := createQuestionInteractionForTest(t, ctx, fixture, callID)

	barrier := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM agent_interactions WHERE agent_id = $1 AND id = $2 FOR NO KEY UPDATE`,
		fixture.AgentID, interaction.ID)
	barrierPID := int32(barrier.Conn().PgConn().PID())
	resolved := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, ID: interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}},
			Actor:      mustOmnaraActorParams(t, fixture.UserID),
		})
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, store.pool, "-- name: ResolveAgentInteraction", barrierPID)
	released := integrationdb.RunAsync(func() (struct{}, error) {
		return struct{}{}, store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, fixture.AgentID, fixture.Lock.ID)
	})
	integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, store.pool, "LockAgentInProject", barrierPID, 1)
	// A second parent writer must remain serialized with release, while the
	// child's foreign-key reference check can pass both waiting writers.
	parentWrite := integrationdb.RunAsync(func() (struct{}, error) {
		tx := integrationdb.BeginTx(t, ctx, store.pool)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err := dbsqlc.New(tx).LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
			ProjectID: testProjectID, ID: parent.ID,
		})
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, tx.Commit(ctx)
	})
	integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, store.pool, "LockAgentInProject", barrierPID, 2)
	require.NoError(t, barrier.Commit(ctx))
	resolution := integrationdb.Await(t, resolved, "question resolution")
	require.NoError(t, resolution.Err)
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolution.Value.State)
	require.NoError(t, integrationdb.Await(t, released, "child runtime release").Err)
	require.NoError(t, integrationdb.Await(t, parentWrite, "parent writer").Err)
	call, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, callID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, call.State)
	var runtimes, wakeups int
	require.NoError(t, store.pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM agent_runtime_locks WHERE agent_id = $1),
  (SELECT count(*) FROM agent_wakeups WHERE agent_id = $1)`, fixture.AgentID).Scan(&runtimes, &wakeups))
	require.Zero(t, runtimes, "resolved child must not retain a stranded runtime")
	require.Equal(t, 1, wakeups, "answered child must remain schedulable")
}
