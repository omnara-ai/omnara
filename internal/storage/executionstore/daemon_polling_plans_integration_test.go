//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestDaemonPollingPlans(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	startQueuedExpiryProcess(t, ctx, f)
	tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/processes.sql.go", "ListDaemonProcessOffers")
	prefix := fmt.Sprintf("'%s','%s',600,", f.OrgID, f.MachineID)
	runtime := fmt.Sprintf("'%s','%s'", f.RuntimeID, f.TokenID)
	_, err := tx.Exec(ctx, "PREPARE polling AS "+query)
	require.NoError(t, err)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode)
		require.NoError(t, err)
		var times []float64
		for i := range 5 {
			var raw json.RawMessage
			require.NoError(t, tx.QueryRow(ctx,
				"EXPLAIN (ANALYZE,SUMMARY,FORMAT JSON) EXECUTE polling("+prefix+runtime+",100)").Scan(&raw))
			var plans []struct {
				Planning  float64 `json:"Planning Time"`
				Execution float64 `json:"Execution Time"`
			}
			require.NoError(t, json.Unmarshal(raw, &plans))
			times = append(times, plans[0].Planning)
			t.Logf("%s sample %d: planning=%.3fms execution=%.3fms", mode, i, plans[0].Planning, plans[0].Execution)
		}
		if mode == "force_custom_plan" {
			slices.Sort(times)
			require.Less(t, times[len(times)/2], 20.0)
		}
	}
	_, err = tx.Exec(ctx, "DEALLOCATE polling")
	require.NoError(t, err)
}

func TestDaemonPollingOffersEquivalent(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	tools := createToolCallBatchForProcessTest(t, ctx, f, t.Name(), []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("shell-one", "run_command"),
		builtInProcessToolCallBatchItem("shell-two", "run_command"),
		builtInProcessToolCallBatchItem("upload", "storage_upload"),
	})
	var offered []uuid.UUID
	for _, id := range tools[:2] {
		offered = append(offered, startQueuedExpiryProcessForTool(t, ctx, f, id).ID)
	}
	upload, err := startProcessForTest(ctx, f.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: tools[2], RuntimeLockID: f.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: f.BindingID, Cwd: "/work",
		ExecutionSpec: processcmd.ForFileTransfer(processcmd.FileTransfer{
			Direction: processcmd.FileTransferUpload, LocalPath: "file.txt",
			Target: processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{}},
		}),
	})
	require.NoError(t, err)
	offered = append(offered, upload.ID)
	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/processes.sql.go", "ListDaemonProcessOffers")
	for _, test := range []struct {
		name, mutation string
		limit          int
		expected       []uuid.UUID
		wrongToken     bool
	}{
		{name: "offered_and_non_shell", limit: 100, expected: offered},
		{name: "limit", limit: 1, expected: offered[:1]},
		{name: "revoked_binding", limit: 100,
			mutation: "UPDATE agent_machine_bindings SET state='released' WHERE agent_id=$1"},
		{name: "revoked_grant", limit: 100,
			mutation: `DELETE FROM project_machine_grants
WHERE project_id=(SELECT project_id FROM agents WHERE id=$1)`},
		{name: "offline_runtime", limit: 100,
			mutation: `UPDATE daemon_runtimes SET state='ended',ended_at=statement_timestamp()
WHERE machine_id=(SELECT machine_id FROM agent_machine_bindings WHERE agent_id=$1)`},
		{name: "wrong_token", wrongToken: true, limit: 100},
		{name: "expired_queue", limit: 100,
			mutation: `UPDATE processes SET created_at=statement_timestamp()-interval '1 hour' WHERE agent_id=$1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
			if test.mutation != "" {
				_, err := tx.Exec(ctx, test.mutation, f.AgentID)
				require.NoError(t, err)
			}
			token := f.TokenID
			if test.wrongToken {
				token = uuid.New()
			}
			rows, err := tx.Query(ctx, query, f.OrgID, f.MachineID, 600, f.RuntimeID, token, test.limit)
			require.NoError(t, err)
			offers, err := pgx.CollectRows(rows, pgx.RowToMap)
			require.NoError(t, err)
			var ids []uuid.UUID
			for _, offer := range offers {
				id, ok := offer["id"].([16]byte)
				require.True(t, ok)
				ids = append(ids, uuid.UUID(id))
			}
			require.Equal(t, test.expected, ids)
		})
	}
}
