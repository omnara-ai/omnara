//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestOpeningMembersPreserveOrderAndMembership(t *testing.T) {
	store, agent := historyCostFixture(t)
	ctx := t.Context()
	_, err := store.pool.Exec(ctx, `SELECT history_cost_turn($1,k,false) FROM generate_series(0,1) k`, agent)
	require.NoError(t, err)
	var inputs []uuid.UUID
	require.NoError(t, store.pool.QueryRow(ctx, `SELECT array_agg(input.id ORDER BY event.sequence)
FROM agent_inputs input JOIN agent_events event ON event.agent_id=input.agent_id AND event.id=input.admitted_event_id
WHERE input.agent_id=$1 AND input.input_kind='content'`, agent).Scan(&inputs))
	require.Len(t, inputs, 2)
	var foreign, received uuid.UUID
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT id FROM agent_inputs WHERE agent_id<>$1 LIMIT 1`, agent).Scan(&foreign))
	require.NoError(t, store.pool.QueryRow(ctx, `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
VALUES ($1,$2,'received','content','queued',now()) RETURNING id`, testProjectID, agent).Scan(&received))
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, test := range []struct {
			name    string
			members []uuid.UUID
			first   int64
			valid   bool
		}{
			{"single", inputs[1:], 10, true},
			{"ordered", inputs, 2, true},
			{"reversed", []uuid.UUID{inputs[1], inputs[0]}, 10, false},
			{"duplicate", []uuid.UUID{inputs[0], inputs[0]}, 2, false},
			{"missing", []uuid.UUID{uuid.New()}, 2, false},
			{"foreign", []uuid.UUID{foreign}, 2, false},
			{"unadmitted", []uuid.UUID{received}, 2, false},
			{"wrong_frontier", inputs, 3, false},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				tx := integrationdb.BeginTx(t, ctx, store.pool)
				_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = "+mode)
				require.NoError(t, err)
				_, err = tx.Exec(ctx, `INSERT INTO model_call_contexts(org_id,project_id,agent_id,turn_id,operation_kind,
 attempt_number,agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
 opening_input_ids,opening_event_sequence)
SELECT org_id,project_id,agent_id,turn_id,operation_kind,attempt_number+1,agent_config_id,configured_model_revision_id,
 input_event_sequence,runtime_lock_id,now(),$2::uuid[],$3::bigint
FROM model_call_contexts WHERE agent_id=$1 AND operation_kind='normal' ORDER BY input_event_sequence DESC LIMIT 1`,
					agent, test.members, test.first)
				if test.valid {
					require.NoError(t, err)
				} else {
					assertPgErrorMessage(t, err, "23514", "opening inputs must belong to the agent in event sequence order")
				}
			})
		}
	}
}
