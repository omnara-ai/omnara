//go:build integration

package agentexecution_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/stretchr/testify/require"
)

func TestExecutionBulkAndLoopLinearity(t *testing.T) {
	f := migratedExecutionFixture(t)
	ctx := t.Context()
	for _, mode := range []string{"bulk", "loop"} {
		t.Run(mode, func(t *testing.T) {
			var baseline time.Duration
			for _, n := range []int{1000, 10000} {
				t.Run(fmt.Sprint(n), func(t *testing.T) {
					cell := agentexecution.NewCell("test", f.pool, nil, nil)
					u, err := cell.Begin(ctx)
					require.NoError(t, err)
					defer func() { _ = u.Rollback(ctx) }()
					started := time.Now()
					ids := make([]uuid.UUID, n)
					for i := range ids {
						ids[i] = uuid.New()
					}
					if mode == "bulk" {
						_, err = u.DB().
							Exec(ctx, `
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
    SELECT id,id,$2,$3,'active',$4,$5,$5 FROM unnest($1::uuid[]) id`, ids, f.org, f.project, f.config, f.now)
						require.NoError(t, err)
						_, err = u.DB().
							Exec(ctx, `INSERT INTO agent_execution_state(agent_id,turn_continuable,incomplete_tools)
    SELECT id,false,false FROM unnest($1::uuid[]) id`, ids)
						require.NoError(t, err)
					} else {
						for _, id := range ids {
							_, err = u.DB().Exec(ctx, `
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
     VALUES($1,$1,$2,$3,'active',$4,$5,$5)`, id, f.org, f.project, f.config, f.now)
							require.NoError(t, err)
							require.NoError(t, u.CreatedAgent(f.project, id, uuid.Nil))
							_, err = u.DB().Exec(ctx, `
INSERT INTO agent_execution_state(agent_id,turn_continuable,incomplete_tools) VALUES($1,false,false)
`, id)
							require.NoError(t, err)
						}
					}
					require.NoError(t, u.Commit(ctx, "linearity fixture"))
					elapsed := time.Since(started)
					t.Logf("%s %d agents: %s (%s/agent)", mode, n, elapsed, elapsed/time.Duration(n))
					var count int
					require.NoError(
						t,
						f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_execution_state WHERE agent_id=ANY($1)`, ids).
							Scan(&count),
					)
					require.Equal(t, n, count)
					if n == 1000 {
						baseline = elapsed
					} else {
						require.Less(t, elapsed, baseline*25+time.Second)
					}
				})
			}
		})
	}
}

func TestExecutionReconstructionTenThousandAgents(t *testing.T) {
	f := releasedExecutionFixture(t)
	ctx := t.Context()
	_, err := f.pool.Exec(
		ctx,
		`INSERT INTO agents(org_id,project_id,state,current_config_id,created_at,updated_at)
 SELECT $1,$2,'active',$3,$4,$4 FROM generate_series(1,10000)`,
		f.org,
		f.project,
		f.config,
		f.now,
	)
	require.NoError(t, err)
	started := time.Now()
	require.NoError(t, applyExecutionCutover(ctx, f.pool))
	t.Logf("released-51 cutover backfill of 10000 agents: %s", time.Since(started))
	rows, err := f.pool.Query(ctx, `SELECT id FROM agents ORDER BY id`)
	require.NoError(t, err)
	var refs []lifecyclelock.AgentRef
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: f.project, AgentID: id})
	}
	require.NoError(t, rows.Err())
	rows.Close()
	cell := agentexecution.NewCell("test", f.pool, nil, nil)
	u, err := cell.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(ctx) }()
	started = time.Now()
	plan, err := u.PlanAgents(ctx, refs, agentexecution.RepairAuthority{})
	require.NoError(t, err)
	require.NoError(t, u.LockAgents(ctx, plan))
	var firstThousand time.Duration
	for i, ref := range refs {
		h, handleErr := u.Agent(
			agentexecution.AgentRoute{
				CellID:      cell.ID(),
				ProjectID:   f.project,
				AgentID:     ref.AgentID,
				RootAgentID: ref.AgentID,
			},
		)
		require.NoError(t, handleErr)
		snapshot, loadErr := h.ReconstructExecution(ctx)
		require.NoError(t, loadErr)
		require.Equal(t, agentexecution.WaitIdle, snapshot.Selection.Wait)
		if i == 999 {
			firstThousand = time.Since(started)
			started = time.Now()
		}
	}
	remaining := time.Since(started)
	t.Logf("lock plan and first 1000 reconstructions: %s; remaining 9000: %s", firstThousand, remaining)
	require.Less(t, remaining, firstThousand*20+time.Second)
}
