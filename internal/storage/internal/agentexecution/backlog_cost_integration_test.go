//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionBacklogCosts(t *testing.T) {
	f, agent, _ := commandFixture(t)
	other := f.create(t, uuid.New(), uuid.Nil)
	previous := 0
	for _, size := range []int{100, 10000} {
		_, err := f.pool.Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,input_rank,queued_at,canceled_at)
SELECT $1,a.id,CASE WHEN history THEN 'canceled' ELSE 'received' END,'content',mode,n*1024,now(),
 CASE WHEN history THEN now() END
FROM (VALUES($2::uuid),($3::uuid)) a(id)
CROSS JOIN (VALUES('queued'),('steering')) m(mode)
CROSS JOIN (VALUES(false),(true)) h(history)
CROSS JOIN LATERAL generate_series($4::int*CASE WHEN history THEN 10 ELSE 1 END+1,
 $5::int*CASE WHEN history THEN 10 ELSE 1 END) n
WHERE mode='queued' OR a.id=$3
`, f.project, agent, other, previous, size)
		require.NoError(t, err)
		_, err = f.pool.Exec(t.Context(), `
INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,ordinal,block_kind,text_content,created_at)
SELECT i.agent_id,'agent_input',i.id,0,'text','pending',i.queued_at FROM agent_inputs i
WHERE i.agent_id=$1 AND i.state='received' AND i.input_rank>$2
`, agent, previous*1024)
		require.NoError(t, err)
		_, err = f.pool.Exec(t.Context(), `ANALYZE agent_inputs; ANALYZE agents; ANALYZE content_blocks`)
		require.NoError(t, err)
		previous = size
		var first, second, last uuid.UUID
		require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT (array_agg(id ORDER BY input_rank))[1],(array_agg(id ORDER BY input_rank))[2],
 (array_agg(id ORDER BY input_rank DESC))[1] FROM agent_inputs WHERE agent_id=$1 AND state='received'
`, agent).Scan(&first, &second, &last))
		if size == 10000 {
			t.Run("index_consumers", func(t *testing.T) {
				conn, err := f.pool.Acquire(t.Context())
				require.NoError(t, err)
				defer conn.Release()
				for _, consumer := range []struct{ path, name string }{
					{"../dbsqlc/agent_inputs.sql.go", "ListQueuedBacklogInputs"},
					{"internal/executiondb/commands_runtime.sql.go", "CancelExecutionInputs"},
				} {
					query := generatedQuery(t, consumer.path, consumer.name)
					plans, err := conn.Conn().PgConn().Exec(t.Context(), "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+query).ReadAll()
					require.NoError(t, err)
					require.Len(t, plans, 1)
					generic := string(plans[0].Rows[0][0])
					t.Logf("generic %s %s", consumer.name, generic)
					require.Contains(t, generic, "agent_inputs_received_content_idx")
					require.NotContains(t, generic, "agent_inputs_content_queued_idx")
					require.NotContains(t, generic, "agent_inputs_content_steering_idx")
				}
			})
		}
		for _, command := range []string{"move", "reject_full", "admit", "renumber"} {
			t.Run(command+"/"+strconv.Itoa(size), func(t *testing.T) {
				if command == "renumber" {
					_, err = f.pool.Exec(t.Context(), `UPDATE agent_inputs SET input_rank=1 WHERE agent_id=$1 AND id=$2`, agent, first)
					require.NoError(t, err)
					_, err = f.pool.Exec(t.Context(), `UPDATE agent_inputs SET input_rank=2 WHERE agent_id=$1 AND id=$2`,
						agent, second)
					require.NoError(t, err)
					defer func() {
						_, err := f.pool.Exec(t.Context(), `UPDATE agent_inputs SET input_rank=CASE id WHEN $2 THEN 1024 ELSE 2048 END
WHERE agent_id=$1 AND id IN ($2,$3)`, agent, first, second)
						require.NoError(t, err)
					}()
				}
				u, h := f.handle(t, agent)
				defer func() { _ = u.Rollback(t.Context()) }()
				started := time.Now()
				trace, err := agentexecution.TraceExecutionCommand(u, func() error {
					switch command {
					case "reject_full":
						_, err := h.ReceiveContent(t.Context(), agentexecution.ReceiveContentInput{
							DeliveryMode: "queued", Content: []agentexecution.Content{{Kind: "text", Text: "new"}},
						})
						require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
						return nil
					case "admit":
						admitted, err := h.AdmitInputs(t.Context())
						if err == nil {
							require.Len(t, admitted.Inputs, 1)
							require.Equal(t, first, admitted.Inputs[0].ID)
						}
						return err
					default:
						_, err := h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{
							ID: last, DeliveryMode: "queued", Position: "before", BeforeID: second})
						return err
					}
				})
				require.NoError(t, err)
				elapsed := time.Since(started)
				bound := 4
				if command == "admit" || command == "renumber" {
					bound = 7
				}
				assert.LessOrEqual(t, len(trace), bound)
				if command == "renumber" {
					rows, err := u.DB().Query(t.Context(), `SELECT agent_id,delivery_mode,count(*) FROM agent_inputs
WHERE xmin=pg_current_xact_id()::text::xid GROUP BY agent_id,delivery_mode`)
					require.NoError(t, err)
					var groups int
					for rows.Next() {
						var changedAgent uuid.UUID
						var mode string
						var count int
						require.NoError(t, rows.Scan(&changedAgent, &mode, &count))
						require.Equal(t, agent, changedAgent)
						require.Equal(t, "queued", mode)
						require.Equal(t, size, count)
						groups++
					}
					require.NoError(t, rows.Err())
					rows.Close()
					require.Equal(t, 1, groups)
				}
				require.NoError(t, u.Rollback(t.Context()))
				t.Logf("pending=%d history=%d command=%s statements=%d duration=%s", size, size*10, command, len(trace), elapsed)
				if command == "renumber" {
					var bulk int
					for _, statement := range trace {
						if statement.Affected > 1 {
							bulk++
							assert.EqualValues(t, size, statement.Affected)
						}
					}
					require.Equal(t, 1, bulk)
					require.LessOrEqual(t, len(trace), 7)
				}
				tx, err := f.pool.Begin(t.Context())
				require.NoError(t, err)
				defer func() { _ = tx.Rollback(t.Context()) }()
				_, err = tx.Exec(t.Context(), `SET LOCAL plan_cache_mode=force_generic_plan`)
				require.NoError(t, err)
				var total int
				for _, statement := range trace {
					var raw []byte
					require.NoError(t, tx.QueryRow(t.Context(),
						"EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+statement.Query.SQL, statement.Query.Arguments...).Scan(&raw))
					name, _, _ := strings.Cut(statement.Query.SQL, "\n")
					t.Logf("%s %s", name, raw)
					if strings.Contains(name, "BacklogRank") || strings.Contains(name, "SelectExecutionAdmission") ||
						strings.Contains(name, "CountPendingExecutionContent") {
						plans, err := tx.Conn().PgConn().Exec(t.Context(),
							"EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+statement.Query.SQL).ReadAll()
						require.NoError(t, err)
						require.Len(t, plans, 1)
						generic := string(plans[0].Rows[0][0])
						require.Contains(t, generic, "agent_inputs_received_content_idx")
						require.NotContains(t, generic, `"Node Type": "Seq Scan"`)
						t.Logf("generic %s %s", name, generic)
						if strings.Contains(name, "CountPendingExecutionContent") {
							require.NotContains(t, string(raw), `"Node Type": "Seq Scan"`)
							if size == 10000 {
								require.Contains(t, string(raw), `"Node Type": "Index Only Scan"`)
								require.NotContains(t, string(raw), `"Node Type": "Sort"`)
							}
							var plan []struct {
								Plan struct {
									Plans []struct {
										Rows  float64 `json:"Actual Rows"`
										Plans []struct {
											Rows float64 `json:"Actual Rows"`
										} `json:"Plans"`
									} `json:"Plans"`
								}
							}
							require.NoError(t, json.Unmarshal(raw, &plan))
							require.EqualValues(t, 50, plan[0].Plan.Plans[0].Rows)
							require.EqualValues(t, 50, plan[0].Plan.Plans[0].Plans[0].Rows)
						}
					}
					total += planBuffers(t, raw)
					if command != "renumber" {
						assert.LessOrEqual(t, planBuffers(t, raw), 128, name)
						var plan []struct {
							Plan struct {
								Rows float64 `json:"Actual Rows"`
							}
						}
						require.NoError(t, json.Unmarshal(raw, &plan))
						assert.LessOrEqual(t, plan[0].Plan.Rows, float64(3), name)
					}
				}
				t.Logf("command=%s total_buffers=%d", command, total)
				if command != "renumber" {
					assert.LessOrEqual(t, total, 256)
				}
			})
		}
	}
}
