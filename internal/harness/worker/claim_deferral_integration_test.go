//go:build integration

package worker

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
)

func TestClaimFailureDefersAgent(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		name := "claim"
		if deferred {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			pool := openWorkerIntegrationDB(t, ctx)
			store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
			now := time.Now()
			bad, user := createWorkerAgentWithUser(t, ctx, store, now)
			var profileID, configID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT agent_profile_id,current_config_id FROM agents WHERE id=$1`, bad).Scan(&profileID, &configID))
			launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: workerTestProjectID, ProfileID: profileID, AgentConfigID: configID,
				LaunchedBy: workerTestUserPrincipal(user), IdempotencyKey: "healthy",
			})
			require.NoError(t, err)
			good := launch.Agent.ID
			createWorkerInput(t, ctx, store, bad, user, "bad", now)
			createWorkerInput(t, ctx, store, good, user, "good", now)
			_, err = pool.Exec(ctx,
				`UPDATE agent_wakeups SET ready_at=statement_timestamp()-interval '1 minute' WHERE agent_id=$1`, bad)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `CREATE FUNCTION reject_claim_lease() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.agent_id='`+bad.String()+`'::uuid THEN
  RAISE EXCEPTION 'claim lease rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$`)
			require.NoError(t, err)
			trigger := `CREATE TRIGGER reject_claim_lease BEFORE INSERT ON agent_runtime_locks FOR EACH ROW EXECUTE FUNCTION reject_claim_lease()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER reject_claim_lease AFTER INSERT ON agent_runtime_locks
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_claim_lease()`
			}
			_, err = pool.Exec(ctx, trigger)
			require.NoError(t, err)
			set := metrics.New()
			var logs bytes.Buffer
			w := NewWorker(store.Execution(), nil, Options{
				Metrics: metrics.NewWorkerRecorder(set), Log: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			_, found, err := w.claimNext(ctx)
			var failed *executionstore.ClaimDeferredError
			require.ErrorAs(t, err, &failed)
			require.False(t, found)
			require.Equal(t, bad, failed.AgentID)
			require.Equal(t, workerTestProjectID, failed.ProjectID)
			var constraint *pgconn.PgError
			require.ErrorAs(t, failed.Cause, &constraint)
			require.Equal(t, "23514", constraint.Code)
			require.Equal(t, "claim lease rejected", constraint.Message)
			response := httptest.NewRecorder()
			set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			require.Contains(t, response.Body.String(), "omnara_worker_claim_deferrals_total 1")
			require.Contains(t, logs.String(), `"level":"ERROR"`)
			require.Contains(t, logs.String(), bad.String())
			require.Contains(t, logs.String(), workerTestProjectID.String())
			var delay float64
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT extract(epoch FROM ready_at-statement_timestamp())::float8
FROM agent_wakeups WHERE agent_id=$1`, bad).Scan(&delay))
			require.InDelta(t, executionpolicy.ClaimFailureDelay.Seconds(), delay, 2)
			var contexts int
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1`, bad).Scan(&contexts))
			require.Zero(t, contexts)
			samples, err := store.Execution().SampleExecutionHeads(ctx, uuid.Nil, 10)
			require.NoError(t, err)
			require.Len(t, samples.Samples, 2)
			for _, sample := range samples.Samples {
				require.NoError(t, sample.Err)
				require.Empty(t, sample.Differences)
			}
			next, found, err := w.claimNext(ctx)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, good, next.AgentID)
			createWorkerInput(t, ctx, store, bad, user, "retry activity", now)
			var due bool
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT ready_at<=statement_timestamp() FROM agent_wakeups WHERE agent_id=$1`, bad).Scan(&due))
			require.True(t, due)
		})
	}
}
