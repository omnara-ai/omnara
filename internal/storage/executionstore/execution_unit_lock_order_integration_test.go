//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func executionLockOrderAgents(t *testing.T, parentFirst bool) (processDaemonFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "execution-unit-locks")
	parent := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	child := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if !parentFirst {
		parent, child = child, parent
	}
	original, err := f.Store.Execution().GetAgentInProject(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	unit, err := f.Store.Execution().IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(ctx) }()
	_, err = unit.CreateAgent(
		ctx,
		agentexecution.CreateAgentInput{
			ID:        parent,
			ProjectID: testProjectID,
			ConfigID:  original.CurrentConfigID,
			Name:      "parent",
		},
	)
	require.NoError(t, err)
	_, err = unit.CreateAgent(
		ctx,
		agentexecution.CreateAgentInput{
			ID:          child,
			ProjectID:   testProjectID,
			ConfigID:    original.CurrentConfigID,
			ParentID:    parent,
			SubagentKey: "fork",
			Name:        "child",
		},
	)
	require.NoError(t, err)
	require.NoError(t, unit.Commit(ctx, "seed family"))
	return f, parent, child
}

func assertExecutionAgentOrder(
	t *testing.T, f processDaemonFixture, first, second uuid.UUID, run func(context.Context) error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	_, err := blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, first)
	require.NoError(t, err)
	done := integrationdb.RunAsyncError(func() error { return run(ctx) })
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentInProject", 1)
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, second)
	require.NoError(t, err, "the later agent must remain free while waiting for the first")
	require.NoError(t, blocker.Commit(ctx))
	require.ErrorIs(
		t,
		integrationdb.Await(t, done, "ordered execution mutation"),
		storeerr.ErrRuntimeLockInactive,
	)
}

func TestModelMutationPlansParentBeforeRuntime(t *testing.T) {
	for _, parentFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("parent_first_%t", parentFirst), func(t *testing.T) {
			f, parent, child := executionLockOrderAgents(t, parentFirst)
			first, second := parent, child
			if !parentFirst {
				first, second = child, parent
			}
			runtime, source := uuid.New(), uuid.New()
			for _, operation := range []string{
				"claim normal", "claim compaction", "retry", "prepare",
				"begin compaction", "replace compaction", "terminal compaction",
			} {
				t.Run(operation, func(t *testing.T) {
					assertExecutionAgentOrder(t, f, first, second, func(ctx context.Context) error {
						s := f.Store.Execution()
						switch operation {
						case "claim normal":
							_, err := s.PrepareNormalModelCall(
								ctx,
								executionstore.PrepareNormalModelCallInput{
									ProjectID: testProjectID, AgentID: child, RuntimeLockID: runtime,
									OpeningInputIDs: []uuid.UUID{source}},
							)
							return err
						case "claim compaction":
							_, err := s.ClaimCompactionModelCall(
								ctx,
								executionstore.ClaimCompactionModelCallInput{
									ProjectID: testProjectID, AgentID: child, RuntimeLockID: runtime, ParentContextID: source,
									InputEventSequence: 1, SourceEventSequenceEnd: 1},
							)
							return err
						case "retry":
							_, err := s.ClaimNextModelCallContext(
								ctx,
								executionstore.ClaimNextModelCallContextInput{
									ProjectID:                     testProjectID,
									AgentID:                       child,
									RuntimeLockID:                 runtime,
									PredecessorModelCallContextID: source},
							)
							return err
						case "prepare":
							_, err := s.PrepareNormalModelCall(
								ctx,
								executionstore.PrepareNormalModelCallInput{
									ProjectID:       testProjectID,
									AgentID:         child,
									RuntimeLockID:   runtime,
									OpeningInputIDs: []uuid.UUID{source}},
							)
							return err
						case "begin compaction":
							_, err := s.RecordModelCallFailureAndClaimCompaction(ctx,
								executionstore.RecordModelCallFailureAndClaimCompactionInput{
									ParentContextID: source, SourceEventSequenceEnd: 1,
									Failure: executionstore.RecordRecoverableModelCallFailureInput{
										ProjectID: testProjectID, AgentID: child, RuntimeLockID: runtime, ModelCallContextID: source,
										RecoveryKind: executionstore.ModelCallRecoveryCompact,
										ErrorKind:    "context_length", ErrorMessage: "too large"}})
							return err
						case "replace compaction":
							_, err := s.ReplaceCompactionSource(
								ctx,
								executionstore.ReplaceCompactionSourceInput{
									ProjectID: testProjectID, AgentID: child, RuntimeLockID: runtime, ModelCallContextID: source,
									NextSourceEventSequenceEnd: 1, ErrorKind: "context_length", ErrorMessage: "too large"},
							)
							return err
						default:
							return s.RecordTerminalCompactionFailure(
								ctx,
								executionstore.RecordTerminalCompactionFailureInput{
									ProjectID: testProjectID, AgentID: child, RuntimeLockID: runtime, ModelCallContextID: source,
									ErrorKind: "context_length", ErrorMessage: "too large"},
							)
						}
					})
				})
			}
		})
	}
}

func TestStopSubagentPlansCallerAndTreeBeforeMutation(t *testing.T) {
	for _, parentFirst := range []bool{true, false} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("parent_first_%t_archive_%t", parentFirst, archive), func(t *testing.T) {
				f, parent, child := executionLockOrderAgents(t, parentFirst)
				first, second := parent, child
				if !parentFirst {
					first, second = child, parent
				}
				assertExecutionAgentOrder(t, f, first, second, func(ctx context.Context) error {
					_, err := f.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
						ProjectID: testProjectID, AgentID: parent, ToolCallID: uuid.New(), RuntimeLockID: uuid.New(),
					}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
						return executionstore.StopSubagentForToolCall(executionstore.StopSubagentInput{
							TargetAgentID: child, Archive: archive,
						}, executionstore.ToolCallCompletionInput{}), nil
					})
					return err
				})
				agent, err := f.Store.Execution().GetAgentInProject(t.Context(), testProjectID, child)
				require.NoError(t, err)
				require.Equal(t, executionstore.AgentStateActive, agent.State)
			})
		}
	}
}

func TestSubagentLaunchLocksMachinesBeforeParentAndInsert(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "subagent-machine-order")
	parent, err := f.Store.Execution().GetAgentInProject(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	_, err = blocker.Exec(ctx, `SELECT id FROM machines WHERE id=$1 FOR UPDATE`, f.MachineID)
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return spawnSubagentForTest(
			t,
			ctx,
			f.Store,
			parent,
			parent.CurrentConfigID,
			"helper",
			"machine-order-child",
			nil,
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockMachineForLifecycle", 1)
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, parent.ID)
	require.NoError(t, err)
	var children int
	require.NoError(t, blocker.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE parent_agent_id=$1`, parent.ID).Scan(&children))
	require.Zero(t, children)
	require.NoError(t, blocker.Commit(ctx))
	outcome := integrationdb.Await(t, done, "subagent launch")
	require.NoError(t, outcome.Err)
	require.Equal(t, parent.ID, outcome.Value.Agent.ParentAgentID)
	require.Len(t, outcome.Value.MachineBindings, 1)
}

func TestSubagentLaunchTakesQuotaBeforeParentAndMachines(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "subagent-quota-order")
	parent, err := f.Store.Execution().GetAgentInProject(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	require.NoError(t, dbsqlc.New(blocker).LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
		ResourceKind: "agents", Scope: testProjectID.String(),
	}))
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return spawnSubagentForTest(
			t,
			ctx,
			f.Store,
			parent,
			parent.CurrentConfigID,
			"helper",
			"quota-order-child",
			nil,
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockResourceCreation", 1)
	_, err = blocker.Exec(ctx, `SELECT id FROM machines WHERE id=$1 FOR UPDATE NOWAIT`, f.MachineID)
	require.NoError(t, err)
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, parent.ID)
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, done, "subagent launch after quota").Err)
}

func TestPreparationFencesLeaseAfterParentWait(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, parent, child := executionLockOrderAgents(t, true)
	runtime, err := f.Store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, child, testWorkerProcessID, time.Minute,
	)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, parent)
	require.NoError(t, err)
	done := integrationdb.RunAsyncError(func() error {
		_, err := f.Store.Execution().PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
			ProjectID:       testProjectID,
			AgentID:         child,
			RuntimeLockID:   runtime.ID,
			OpeningInputIDs: []uuid.UUID{uuid.New()},
		})
		return err
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentInProject", 1)
	expireAgentRuntimeLockForTest(t, ctx, f.Store, runtime.ID)
	require.NoError(t, blocker.Commit(ctx))
	require.ErrorIs(
		t,
		integrationdb.Await(t, done, "preparation after lease expiry"),
		storeerr.ErrRuntimeLockInactive,
	)
}

func TestSubagentLaunchReplansAfterMachineTeardown(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "subagent-machine-teardown")
	parent, err := f.Store.Execution().GetAgentInProject(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	teardown, err := f.Store.Execution().IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = teardown.Rollback(ctx) }()
	_, err = dbsqlc.New(teardown.DB()).LockMachineForLifecycle(ctx, dbsqlc.LockMachineForLifecycleParams{
		OrgID: testOrgID, ID: f.MachineID,
	})
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return spawnSubagentForTest(
			t,
			ctx,
			f.Store,
			parent,
			parent.CurrentConfigID,
			"helper",
			"teardown-child",
			nil,
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockMachineForLifecycle", 1)
	_, err = teardown.DB().Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE NOWAIT`, parent.ID)
	require.NoError(t, err)
	_, err = f.Store.Execution().DeleteMachineInUnit(ctx, teardown, executionstore.DeleteMachineInput{
		OrgID: testOrgID, MachineID: f.MachineID,
	})
	require.NoError(t, err)
	require.NoError(t, teardown.Commit(ctx, "teardown before launch"))
	result := integrationdb.Await(t, done, "launch after machine teardown")
	require.NoError(t, result.Err)
	require.True(t, result.Value.Created)
	require.Empty(t, result.Value.MachineBindings)
	var children int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE parent_agent_id=$1`, parent.ID).Scan(&children))
	require.Equal(t, 1, children)
}
