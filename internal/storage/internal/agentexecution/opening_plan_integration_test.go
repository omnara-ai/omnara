//go:build integration

package agentexecution_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCaptureOpeningPlanWithStaleInputStatistics(t *testing.T) {
	f, agent, _ := commandFixture(t)
	other := f.create(t, uuid.New(), uuid.Nil)
	u, h := f.handle(t, agent)
	ctx := t.Context()
	_, err := u.DB().Exec(ctx, `ALTER TABLE agent_inputs SET (autovacuum_enabled = false); ANALYZE agent_inputs`)
	require.NoError(t, err)
	_, err = u.DB().Exec(ctx, `INSERT INTO agent_inputs
 (id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 SELECT gen_random_uuid(),$1,$2,'received','content','queued',statement_timestamp()
 FROM generate_series(1,20000)`, f.project, other)
	require.NoError(t, err)
	received := receiveCommand(t, h, "steering", "opening")
	admitted, err := h.AdmitInputs(ctx)
	require.NoError(t, err)
	require.Len(t, admitted.Inputs, 1)
	raw, err := os.ReadFile("queries/inputs.sql")
	require.NoError(t, err)
	query := strings.NewReplacer(
		"sqlc.arg(agent_id)", "$1::uuid",
		"sqlc.arg(watermark)", "$2::bigint",
		"sqlc.arg(stop_sequence)", "$3::bigint",
		"sqlc.arg(turn_id)", "$4::uuid",
	).Replace(string(raw))
	require.NotContains(t, query, "project_id")
	_, err = u.DB().Exec(ctx, "PREPARE opening_plan(uuid,bigint,bigint,uuid) AS "+query)
	require.NoError(t, err)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err = u.DB().Exec(ctx, "SET LOCAL plan_cache_mode="+mode)
		require.NoError(t, err)
		for _, stop := range []int64{0, admitted.Inputs[0].Event.Sequence} {
			var plan []byte
			require.NoError(t, u.DB().QueryRow(ctx,
				"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE opening_plan('"+agent.String()+"',"+
					strconv.FormatInt(admitted.Inputs[0].Event.Sequence, 10)+","+strconv.FormatInt(stop, 10)+
					",'"+admitted.TurnID.String()+"')").Scan(&plan))
			require.NotContains(t, string(plan), "agent_inputs_content_project_queued_idx", "%s", plan)
			require.Contains(t, string(plan), "agent_inputs_agent_id_id_key", "%s", plan)
			var id uuid.UUID
			var sequence int64
			var created any
			require.NoError(t, u.DB().QueryRow(ctx, query, agent, admitted.Inputs[0].Event.Sequence,
				stop, admitted.TurnID).Scan(&id, &sequence, &created))
			require.Equal(t, received.ID, id)
		}
	}
}

func TestExecutionQueueAutovacuumSettings(t *testing.T) {
	f := migratedExecutionFixture(t)
	for table, want := range map[string][]string{
		"agent_inputs":        {"autovacuum_analyze_threshold=1000", "autovacuum_analyze_scale_factor=0.01"},
		"agent_wakeups":       {"autovacuum_vacuum_threshold=1000", "autovacuum_vacuum_scale_factor=0.01"},
		"agent_runtime_locks": {"autovacuum_vacuum_threshold=1000", "autovacuum_vacuum_scale_factor=0.01"},
	} {
		var options []string
		require.NoError(t, f.pool.QueryRow(t.Context(),
			`SELECT reloptions FROM pg_class WHERE oid=$1::regclass`, table).Scan(&options))
		require.ElementsMatch(t, want, options)
	}
}
