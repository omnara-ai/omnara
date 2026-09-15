//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestLaunchAgentQuotaSerializesAdmissionAndPreservesReplay(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		name := "root"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			fixture := newProcessDaemonFixture(t, ctx, "launch_quota_"+name)
			store := fixture.Store
			input := subagentLockTestInput(t, ctx, fixture)
			if !child {
				machine, err := store.Execution().GetMachine(ctx, testOrgID, fixture.MachineID)
				if err != nil {
					t.Fatalf("load explicit machine: %v", err)
				}
				config := mustCreateAgentConfigFromYAML(t, ctx, store, `
instruction: Run on the shared machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: `+machine.DisplayName+`
    cwd: /work
`)
				input.AgentConfigID = config.ID
				input.Subagent = nil
				input.LaunchedBy = userPrincipal(fixture.UserID)
				input.MessageActor = nil
			}
			initialCount, err := store.q.CountActiveAgentsForProject(
				ctx, dbsqlc.CountActiveAgentsForProjectParams{ProjectID: testProjectID},
			)
			if err != nil {
				t.Fatalf("count initial agents: %v", err)
			}
			if _, err := store.pool.Exec(ctx, `INSERT INTO org_resource_limit_overrides(org_id, max_active_agents_per_project)
				VALUES ($1, $2)`, testOrgID, initialCount+1); err != nil {
				t.Fatalf("set one remaining agent slot: %v", err)
			}
			otherConfig := mustCreateAgentConfigFromYAML(t, ctx, store, subagentParentYAML)
			quotaTx := launchLockTestTransaction(t, ctx, store.pool,
				`SELECT pg_advisory_xact_lock(hashtextextended('resource_creation:agents:' || $1::text, 0))`,
				testProjectID.String())
			blockingPID := int32(quotaTx.Conn().PgConn().PID())
			first := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return store.Execution().IntegrationLaunchAgentOnce(ctx, input)
			})
			integrationdb.WaitForLockWaitBlockedBy(t, ctx, store.pool, "-- name: LockResourceCreation", blockingPID)
			replay := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return store.Execution().IntegrationLaunchAgentOnce(ctx, input)
			})
			integrationdb.WaitForNamedLockWaitersBlockedByChain(
				t, ctx, store.pool, "LockAgentLaunchIdempotencyKey", blockingPID, 1,
			)
			otherInput := executionstore.LaunchAgentInput{
				ProjectID: testProjectID, AgentConfigID: otherConfig.ID,
				LaunchedBy: userPrincipal(fixture.UserID), IdempotencyKey: "competing-launch",
			}
			other := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return store.Execution().IntegrationLaunchAgentOnce(ctx, otherInput)
			})
			integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, store.pool, "LockResourceCreation", blockingPID, 2)
			if err := quotaTx.Commit(ctx); err != nil {
				t.Fatalf("release quota: %v", err)
			}
			created := integrationdb.Await(t, first, "first launch")
			if created.Err != nil || !created.Value.Created || len(created.Value.MachineBindings) != 1 {
				t.Fatalf("first launch must take final quota slot: result=%+v err=%v", created.Value, created.Err)
			}
			replayed := integrationdb.Await(t, replay, "concurrent launch replay")
			if replayed.Err != nil {
				t.Fatalf("replay at quota (and sibling limit for child): %v", replayed.Err)
			}
			requireCurrentAgentLaunchReplay(t, replayed.Value, created.Value.Agent)
			rejected := integrationdb.Await(t, other, "competing launch")
			if !errors.Is(rejected.Err, storeerr.ErrConflict) {
				t.Fatalf("competing launch error = %v, want quota conflict", rejected.Err)
			}
			count, err := store.q.CountActiveAgentsForProject(
				ctx, dbsqlc.CountActiveAgentsForProjectParams{ProjectID: testProjectID},
			)
			if err != nil || count != initialCount+1 {
				t.Fatalf("active agents = %d err=%v, want %d", count, err, initialCount+1)
			}
			var inputs, bindings int
			if err := store.pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'),
				(SELECT count(*) FROM agent_machine_bindings WHERE agent_id = $1)`,
				created.Value.Agent.ID,
			).Scan(&inputs, &bindings); err != nil {
				t.Fatalf("count launch side effects: %v", err)
			}
			if inputs != 1 || bindings != 1 {
				t.Fatalf("replay duplicated launch side effects: inputs=%d bindings=%d", inputs, bindings)
			}
			if _, _, err := store.Execution().ArchiveAgent(
				ctx, testProjectID, created.Value.Agent.ID, userPrincipal(fixture.UserID),
			); err != nil {
				t.Fatalf("archive admitted agent: %v", err)
			}
			replacement, err := store.Execution().LaunchAgent(ctx, otherInput)
			if err != nil || !replacement.Created {
				t.Fatalf("archival must release the quota slot and rejected key: result=%+v err=%v", replacement, err)
			}
		})
	}
}

func TestLaunchWaitingForPoolDoesNotBlockUnrelatedLaunch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "launch_agent_quota")
	otherConfig := mustCreateAgentConfigFromYAML(t, ctx, fixture.store, subagentParentYAML)
	poolTx := launchLockTestTransaction(t, ctx, fixture.pool,
		`SELECT id FROM machine_pools WHERE id = $1 FOR UPDATE`, fixture.machinePool.ID)
	input := executionstore.LaunchAgentInput{
		ProjectID: testProjectID, AgentConfigID: fixture.agent.CurrentConfigID,
		LaunchedBy: userPrincipal(fixture.userID), IdempotencyKey: "launch-behind-busy-pool",
	}
	launch := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, input)
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, fixture.pool,
		"-- name: LockMachinePoolForLifecycle", int32(poolTx.Conn().PgConn().PID()))
	other := input
	other.AgentConfigID, other.IdempotencyKey = otherConfig.ID, "launch-without-busy-pool"
	result, err := fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, other)
	if err != nil || !result.Created {
		t.Fatalf("unrelated launch behind busy pool: result=%+v err=%v", result, err)
	}
	if err := poolTx.Commit(ctx); err != nil {
		t.Fatalf("release pool: %v", err)
	}
	outcome := integrationdb.Await(t, launch, "launch after pool release")
	if outcome.Err != nil || !outcome.Value.Created {
		t.Fatalf("launch after pool release: result=%+v err=%v", outcome.Value, outcome.Err)
	}
}
