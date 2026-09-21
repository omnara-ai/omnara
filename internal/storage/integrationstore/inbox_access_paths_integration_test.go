//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

// Explain the actual query source, with bound sqlc parameters, rather than a
// separately maintained SQL approximation. Every data-changing EXPLAIN rolls back.
func explainInboxQuery(t *testing.T, f inboxFixture, name string, parameters map[string]any) inboxQueryPlan {
	t.Helper()
	return explainInboxQueryFile(t, f, "integration_inbox.sql", name, parameters)
}

func explainInboxQueryFile(t *testing.T, f inboxFixture, file, name string, parameters map[string]any) inboxQueryPlan {
	t.Helper()
	query, args := bindInboxQueryFile(t, file, name, parameters)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	var raw []byte
	require.NoError(
		t,
		tx.QueryRow(f.ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&raw),
		"explain %s",
		name,
	)
	var explained []struct{ Plan inboxQueryPlan }
	require.NoError(t, json.Unmarshal(raw, &explained))
	require.Len(t, explained, 1)
	return explained[0].Plan
}

func bindInboxQueryFile(t *testing.T, file, name string, parameters map[string]any) (string, []any) {
	t.Helper()
	source, err := os.ReadFile("../queries/" + file)
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(source), "-- name: "+name+" :")
	require.True(t, found, "query %s", name)
	_, rest, found = strings.Cut(rest, "\n")
	require.True(t, found)
	query, _, _ := strings.Cut(rest, "-- name:")
	var args []any
	positions := map[string]string{}
	query = regexp.MustCompile(`sqlc\.(?:n?arg)\(([a-z_]+)\)`).ReplaceAllStringFunc(query, func(token string) string {
		key := token[strings.IndexByte(token, '(')+1 : len(token)-1]
		if position, ok := positions[key]; ok {
			return position
		}
		value, ok := parameters[key]
		require.True(t, ok, "missing %s parameter", key)
		args = append(args, value)
		position := "$" + strconv.Itoa(len(args))
		positions[key] = position
		return position
	})
	return query, args
}

//nolint:tparallel // Subtests mutate one shared fixture and compare each successive query plan.
func TestInboxSelectionReservationAccessPathIgnoresHistoryAndOtherConversations(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	app := f.appID
	// Completed plans match the requested identity but must not reserve it. Failed
	// unplanned receipts and many unrelated pending/processing/failed plans must
	// not turn either an empty lookup or a late match into a history scan.
	f.exec(t, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,plan,claim_token,claim_expires_at,completed_at)
 SELECT $1::uuid,$2::uuid,'reservation-history:'||n,'x'::bytea,
 CASE WHEN n<=8000 THEN 'completed' WHEN n<=16000 THEN 'failed' WHEN n<=24000 THEN 'pending'
      WHEN n<=28000 THEN 'processing' ELSE 'failed' END,
 CASE WHEN n>8000 AND n<=16000 THEN NULL ELSE
   jsonb_build_object('a',jsonb_build_object('selection',jsonb_build_object(
     'app_id',$3::text,'slot','a',
     'address',jsonb_build_object('kind','thread','ref',
       CASE WHEN n<=8000 THEN 'C123:123.456' ELSE 'C123:'||n||'.456' END)))) END,
 CASE WHEN n>24000 AND n<=28000 THEN uuidv7() ELSE NULL END,
 CASE WHEN n>24000 AND n<=28000 THEN now()+interval '1 minute' ELSE NULL END,
 CASE WHEN n<=8000 THEN now() ELSE NULL END
 FROM generate_series(1,32000) n`, f.project, f.appID, app.String())
	f.exec(t, "ANALYZE integration_inbox")
	identity := map[string]any{
		"app_id":  app,
		"address": map[string]any{"kind": "thread", "ref": "C123:123.456"},
	}
	selection, err := json.Marshal(identity)
	require.NoError(t, err)
	own := uuid.New()
	parameters := map[string]any{
		"project_id": f.project, "app_id": f.appID, "receipt_id": own, "selection": selection,
		"include_failed": true,
	}
	assertLookup := func(t *testing.T, wantID uuid.UUID, wantState string, maxInspected float64) {
		t.Helper()
		query, args := bindInboxQueryFile(t, "app_selections.sql", "FindInboxSelectionReservations", parameters)
		rows, err := f.pool.Query(f.ctx, query, args...)
		require.NoError(t, err)
		defer rows.Close()
		var wantRows float64
		if wantID != uuid.Nil {
			wantRows = 1
			require.True(t, rows.Next(), "missing reservation owner: %v", rows.Err())
			var id uuid.UUID
			var state string
			require.NoError(t, rows.Scan(&id, &state))
			require.Equal(t, wantID, id)
			require.Equal(t, wantState, state, "reservation diagnostics must identify the owner's current state")
		}
		require.False(t, rows.Next(), "unexpected or duplicated reservation owner")
		require.NoError(t, rows.Err())
		rows.Close()
		plan := explainInboxQueryFile(t, f, "app_selections.sql", "FindInboxSelectionReservations", parameters)
		require.Equal(t, wantRows, plan.Rows, "reservation lookup result count")
		assertInboxRowsInspected(t, plan, maxInspected)
		var ginUsed bool
		var walk func(inboxQueryPlan)
		walk = func(node inboxQueryPlan) {
			if node.Index == "integration_inbox_selection_idx" && node.Loops > 0 {
				ginUsed = true
				require.LessOrEqual(
					t,
					node.Rows*node.Loops,
					maxInspected,
					"GIN must identify only matching reservations",
				)
			}
			for _, child := range node.Plans {
				walk(child)
			}
		}
		walk(plan)
		require.True(t, ginUsed, "reservation lookup must use the expression GIN, not scan all plans")
	}
	t.Run("no matching reservation", func(t *testing.T) { assertLookup(t, uuid.Nil, "", 0) })
	// Two slots reserve the same whole recipient set, without returning the same
	// receipt twice. The caller's own otherwise-matching receipt is excluded.
	for i, state := range []string{"pending", "processing", "failed"} {
		t.Run(state, func(t *testing.T) {
			// Separate addresses keep this assertion about unrelated plan scans,
			// independent of GIN retaining dead TIDs until vacuum after an update.
			identity["address"] = map[string]any{"kind": "thread", "ref": "C123:123." + strconv.Itoa(456+i)}
			selection, err := json.Marshal(identity)
			require.NoError(t, err)
			own, match := uuid.New(), uuid.New()
			parameters["receipt_id"], parameters["selection"] = own, selection
			f.exec(t, `INSERT INTO integration_inbox
 (id,project_id,app_id,receipt_key,payload,plan,state,claim_token,claim_expires_at)
 SELECT id,$1,$2,'matching:'||id,'x'::bytea,
   jsonb_build_object('a',jsonb_build_object('selection',$5::jsonb||'{"slot":"a"}'::jsonb),
                      'b',jsonb_build_object('selection',$5::jsonb||'{"slot":"b"}'::jsonb)), $6::text,
   CASE WHEN $6::text='processing' THEN uuidv7() ELSE NULL END,
   CASE WHEN $6::text='processing' THEN now()+interval '1 minute' ELSE NULL END
 FROM unnest(ARRAY[$3::uuid,$4::uuid]) id`, f.project, f.appID, own, match, selection, state)
			assertLookup(t, match, state, 2)
			parameters["include_failed"] = false
			if state == "failed" {
				assertLookup(t, uuid.Nil, "", 2)
			} else {
				assertLookup(t, match, state, 2)
			}
			parameters["include_failed"] = true
		})
	}
}

type inboxQueryPlan struct {
	NodeType string           `json:"Node Type"`
	Relation string           `json:"Relation Name"`
	Index    string           `json:"Index Name"`
	Rows     float64          `json:"Actual Rows"`
	Loops    float64          `json:"Actual Loops"`
	Filtered float64          `json:"Rows Removed by Filter"`
	Plans    []inboxQueryPlan `json:"Plans"`
}

func assertInboxRowsInspected(t *testing.T, plan inboxQueryPlan, maxRows float64) {
	t.Helper()
	var inspected float64
	var walk func(inboxQueryPlan)
	walk = func(node inboxQueryPlan) {
		if node.Relation == "integration_inbox" && node.NodeType != "ModifyTable" {
			inspected += (node.Rows + node.Filtered) * node.Loops
			if node.Loops > 0 {
				require.NotEqual(t, "Seq Scan", node.NodeType, "inbox poll must not scan live/retained history")
			}
		}
		for _, child := range node.Plans {
			walk(child)
		}
	}
	walk(plan)
	require.LessOrEqual(t, inspected, maxRows, "inbox poll inspected healthy or retained rows")
}

//nolint:tparallel // Empty-poll checks must finish before the parent adds expired and inactive receipts.
func TestInboxPollAccessPathsIgnoreHealthyPendingAndHistory(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	disabled := f.addApp(t, "disconnected", integrationstore.ProjectAppSettings{}).ID
	f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, disabled)
	other := f.addApp(t, "other-ready", integrationstore.ProjectAppSettings{}).ID
	// Future work, recent completions, failed selections on an inactive app,
	// and another app's ready backlog must not make empty polls scan history.
	f.exec(
		t,
		`INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload,state,available_at,completed_at)
 SELECT $1,CASE WHEN n>24000 THEN $4::uuid WHEN n>16000 THEN $3::uuid ELSE $2::uuid END,'bulk:'||n,'x'::bytea,
 CASE WHEN n<=8000 OR n>24000 THEN 'pending' WHEN n<=16000 THEN 'completed' ELSE 'failed' END,
 CASE WHEN n<=8000 THEN now()+interval '1 day' ELSE now() END,
 CASE WHEN n>8000 AND n<=16000 THEN now() ELSE NULL END
 FROM generate_series(1,32000) n`,
		f.project,
		f.appID,
		disabled,
		other,
	)
	f.exec(t, "ANALYZE integration_inbox")
	f.exec(t, "ANALYZE project_apps")
	for _, name := range []string{
		"RecoverExpiredIntegrationInboxReceipts", "FailInactiveIntegrationInboxReceipts",
		"CleanupDeletedIntegrationInboxReceipts",
	} {
		t.Run(name, func(t *testing.T) {
			assertInboxRowsInspected(t, explainInboxQuery(t, f, name, map[string]any{"row_limit": 100}), 0)
		})
	}
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "OldestReadyIntegrationInboxLag", nil), 1)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "ListReadyIntegrationInboxApps", map[string]any{
		"row_limit": 100,
	}), 100)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "CleanupTerminalIntegrationInboxReceipts", map[string]any{
		"row_limit": 100, "retention_milliseconds": time.Hour.Milliseconds(),
	}), 0)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "ClaimIntegrationInboxReceipt", map[string]any{
		"project_id": f.project, "app_id": f.appID,
		"claim_token": uuid.New(), "lease_milliseconds": int64(60000),
	}), 0)
	// A single expired lease stays bounded even with thousands of healthy rows.
	f.accept(t, "expired")
	r := f.claim(t)
	f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, r.ID)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "RecoverExpiredIntegrationInboxReceipts", map[string]any{
		"row_limit": 1,
	}), 2)
	f.exec(t, `INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload)
 VALUES($1,$2,'inactive','x'::bytea)`, f.project, disabled)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "FailInactiveIntegrationInboxReceipts", map[string]any{
		"row_limit": 1,
	}), 2)
	f.exec(t, `UPDATE project_apps SET deleted_at=now() WHERE id=$1`, disabled)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "CleanupDeletedIntegrationInboxReceipts", map[string]any{
		"row_limit": 1,
	}), 2)
	// Empty lag sampling also seeks the ready index rather than scanning future
	// retries or terminal history. All other access-path checks above are done.
	f.exec(t, `UPDATE integration_inbox SET available_at=now()+interval '1 day' WHERE state='pending'`)
	f.exec(t, "VACUUM ANALYZE integration_inbox")
	assertInboxRowsInspected(t, explainInboxQuery(t, f, "OldestReadyIntegrationInboxLag", nil), 0)
}

func TestInboxInactiveRecoveryBatchesAppsAcrossProjects(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	otherProject := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, otherProject,
		"Other inbox project", "other-inbox-project", time.Now())
	// Each project has four inactive apps with retained failures and an
	// active app with a large healthy backlog. Arrays are real batches;
	// a match in either project must not inspect unrelated apps' receipts.
	for _, project := range []uuid.UUID{f.project, otherProject} {
		for app := range 5 {
			id := uuid.New()
			state, inboxState := "disconnected", "failed"
			if app == 4 {
				state, inboxState = "active", "pending"
			}
			f.exec(t, `INSERT INTO project_apps
 (id,org_id,project_id,installed_by_user_id,provider,state,
  provider_tenant_id,provider_account_ref,name,definition_id,credential_secret_id,created_at,updated_at)
 VALUES($1,$2,$3,$4,'slack',$5,'batch-team',($1::uuid)::text,'app-'||$7::text,'omnara.slack',
 (SELECT credential_secret_id FROM project_apps WHERE id=$6),now(),now())`,
				id, f.org, project, f.user, state, f.appID, strconv.Itoa(app))
			f.exec(t, `INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload,state)
 SELECT $1,$2,'batch-history:'||n,'x'::bytea,$3 FROM generate_series(1,4000) n`, project, id, inboxState)
		}
	}
	f.exec(t, "ANALYZE project_apps")
	f.exec(t, "ANALYZE integration_inbox")
	name := "FailInactiveIntegrationInboxReceipts"
	assertInboxRowsInspected(t, explainInboxQuery(t, f, name, map[string]any{"row_limit": 3}), 0)
	f.exec(t, `INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload)
 SELECT project_id,id,'batch-pending','x'::bytea FROM project_apps WHERE state='disconnected'`)
	assertInboxRowsInspected(t, explainInboxQuery(t, f, name, map[string]any{"row_limit": 3}), 6)
	// Both the lateral and outer limit matter: three rows total, across all
	// projects. Repeated bounded batches must eventually drain every app.
	query, args := bindInboxQueryFile(t, "integration_inbox.sql", name, map[string]any{"row_limit": 3})
	for _, want := range []int64{3, 3, 2, 0} {
		result, err := f.pool.Exec(f.ctx, query, args...)
		require.NoError(t, err)
		require.Equal(t, want, result.RowsAffected())
	}
	var remaining int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_inbox WHERE state='pending'`).Scan(&remaining))
	require.Equal(t, 8000, remaining, "healthy pending receipts remain unchanged")
}
