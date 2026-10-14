//go:build integration

package worker

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type delayedOwnedStore struct {
	Store
	after func()
}

func (s delayedOwnedStore) AdvanceOwnedAgentWork(ctx context.Context,
	input executionstore.AdvanceOwnedAgentWorkInput) (executionstore.ClaimedAgentWork,
	bool,
	error) {
	work, continued, err := s.Store.AdvanceOwnedAgentWork(ctx, input)
	if continued && work.Model.Prepared != nil && s.after != nil {
		s.after()
	}
	return work, continued, err
}

type delayedFusedExecutor struct {
	kernel.AgentExecutor
	before, after func()
	observe       func(executionstore.OwnedAgentWorkTransition)
}

func (e delayedFusedExecutor) ExecuteModelWorkAndAdvance(ctx context.Context,
	input kernel.ModelWorkExecution,
	options kernel.ModelWorkAdvanceOptions) (executionstore.OwnedAgentWorkTransition,
	bool,
	error) {
	if e.before != nil {
		e.before()
		options.Deadline = time.Unix(1, 0)
	}
	work, advanced, err := e.AgentExecutor.ExecuteModelWorkAndAdvance(ctx, input, options)
	if e.observe != nil {
		e.observe(work)
	}
	if work.Continued && work.Work.Model.Prepared != nil && e.after != nil {
		e.after()
	}
	return work, advanced, err
}

func TestWorkerContinuationDurationNeverAbandonsPreparedContext(t *testing.T) {
	for _, path := range []string{"owned", "fused"} {
		for _, boundary := range []string{"before", "during", "after"} {
			t.Run(path+"/"+boundary, func(t *testing.T) {
				ctx := t.Context()
				base := openWorkerIntegrationDB(t, ctx)
				trace := &integrationdb.QueryTrace{}
				config := base.Config()
				config.ConnConfig.Tracer = multitracer.New(config.ConnConfig.Tracer, trace)
				pool, err := pgxpool.NewWithConfig(ctx, config)
				require.NoError(t, err)
				defer pool.Close()
				var worker *Worker
				store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
				agent, user := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
				createWorkerInput(t, ctx, store, agent, user, "continue", time.Now())
				first := continuationFinalResponse(1)
				first.StopReason = model.StopReasonMaxTokens
				executor := newObservedContinuationExecutor(store, first, continuationFinalResponse(2))
				pause := func() { worker.continuation.MaxDuration = 0 }
				var workerStore Store = store.Execution()
				var workerExecutor AgentWorkExecutor = executor
				if path == "owned" {
					if boundary == "before" {
						executor.afterModel = func(context.Context) error { pause(); return nil }
					}
					if boundary == "after" {
						workerStore = delayedOwnedStore{Store: workerStore, after: pause}
					}
				} else {
					fused := delayedFusedExecutor{AgentExecutor: executor.AgentExecutor}
					if boundary == "before" {
						fused.before = pause
					}
					if boundary == "after" {
						fused.after = pause
					}
					workerExecutor = fused
				}
				if boundary == "during" {
					starts := 0
					trace.OnQuery = func(query string) {
						if strings.HasPrefix(query, "-- name: CreateExecutionAttempt ") {
							starts++
							if starts == 2 {
								pause()
							}
						}
					}
				}
				worker = NewWorker(workerStore,
					workerExecutor,
					Options{Continuation: ContinuationOptions{MaxModelStarts: 2,
						MaxDuration: time.Hour}})
				requireWorkerClaim(t, ctx, worker, "duration boundary")
				var contexts, outputs, failed int
				require.NoError(t,
					pool.QueryRow(ctx,
						`SELECT count(*),count(*) FILTER(WHERE state='failed') FROM model_call_contexts WHERE agent_id=$1`,
						agent).Scan(&contexts,
						&failed))
				require.Zero(t, failed)
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM model_outputs WHERE agent_id=$1`, agent).Scan(&outputs))
				require.Equal(t, contexts, outputs)
				expected := 2
				if boundary == "before" {
					expected = 1
				}
				require.Equal(t, expected, outputs)
				if boundary == "before" {
					requireWorkerClaim(t, ctx, worker, "resume yielded work")
				}
				require.NoError(t,
					pool.QueryRow(ctx,
						`SELECT count(*),count(*) FILTER(WHERE state='failed') FROM model_call_contexts WHERE agent_id=$1`,
						agent).Scan(&contexts,
						&failed))
				require.Equal(t, 2, contexts)
				require.Zero(t, failed)
				waitNoWorkerWakeup(t, ctx, pool, agent)
			})
		}
	}
}

func TestWorkerAcceptsResponseWhenSuccessorPreparationFails(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		name := "immediate"
		if deferred {
			name = "deferred"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			pool := openWorkerIntegrationDB(t, ctx)
			store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
			agent, user := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
			createWorkerInput(t, ctx, store, agent, user, "continue", time.Now())
			first := continuationFinalResponse(1)
			first.StopReason = model.StopReasonMaxTokens
			executor := newObservedContinuationExecutor(store, first, continuationFinalResponse(2))
			_, err := pool.Exec(ctx, `CREATE FUNCTION reject_successor() RETURNS trigger LANGUAGE plpgsql AS $$
   BEGIN IF EXISTS(SELECT 1 FROM model_outputs WHERE agent_id=NEW.agent_id) THEN
 RAISE EXCEPTION 'reject successor'; END IF; RETURN NEW; END $$`)
			require.NoError(t, err)
			trigger := `CREATE TRIGGER reject_successor BEFORE INSERT ON model_call_contexts
 FOR EACH ROW EXECUTE FUNCTION reject_successor()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER reject_successor AFTER INSERT ON model_call_contexts
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION reject_successor()`
			}
			_, err = pool.Exec(ctx, trigger)
			require.NoError(t, err)
			var logged bytes.Buffer
			executor.StreamLog = slog.New(slog.NewJSONHandler(&logged, nil))
			var transition executionstore.OwnedAgentWorkTransition
			observed := delayedFusedExecutor{AgentExecutor: executor.AgentExecutor,
				observe: func(result executionstore.OwnedAgentWorkTransition) { transition = result }}
			worker := NewWorker(store.Execution(),
				observed,
				Options{Continuation: ContinuationOptions{MaxModelStarts: 2,
					MaxDuration: 30 * time.Second}})
			worked, runErr := worker.RunOnce(ctx)
			require.True(t, worked)
			var outputs, failed int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM model_outputs WHERE agent_id=$1`, agent).Scan(&outputs))
			require.Equal(t, 1, outputs)
			require.NoError(t, runErr)
			var cause *pgconn.PgError
			require.ErrorAs(t, transition.Fallback, &cause)
			require.Equal(t, "reject successor", cause.Message)
			require.Contains(t, logged.String(), `"level":"WARN"`)
			require.Contains(t, logged.String(), `"agent_id":"`+agent.String()+`"`)
			require.Contains(t, logged.String(), `"model_call_context_id":`)
			require.Contains(t, logged.String(), `"runtime_lock_id":`)
			require.Contains(t, logged.String(), "SQLSTATE P0001")
			require.NoError(t,
				pool.QueryRow(ctx,
					`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND state='failed'`,
					agent).Scan(&failed))
			require.Zero(t, failed)
			_, err = pool.Exec(ctx, `DROP TRIGGER reject_successor ON model_call_contexts`)
			require.NoError(t, err)
			requireWorkerClaim(t, ctx, worker, "resume after rejected successor")
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM model_outputs WHERE agent_id=$1`, agent).Scan(&outputs))
			require.Equal(t, 2, outputs)
			waitNoWorkerWakeup(t, ctx, pool, agent)
		})
	}
}
