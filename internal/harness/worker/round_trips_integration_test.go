//go:build integration

package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestWorkerModelToolChainPoolAcquisitions(t *testing.T) {
	ctx := t.Context()
	fixture := openWorkerIntegrationDB(t, ctx)
	cfg := fixture.Config()
	trace := &stepCommitTracer{}
	cfg.ConnConfig.Tracer = multitracer.New(cfg.ConnConfig.Tracer, trace)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	createWorkerInput(t, ctx, store, agentID, userID, "continue", time.Now())
	executor := newObservedContinuationExecutor(store,
		continuationToolResponse(1), continuationToolResponse(2), continuationFinalResponse(3))
	executor.ModelResolver = credentialResolvingTestModel{
		resolver: modelprovider.Resolver{Models: store.Models(), Secrets: store.Secrets()},
		client: &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
			continuationToolResponse(1), continuationToolResponse(2), continuationFinalResponse(3),
		}},
	}
	worker := NewWorker(store.Execution(), executor.AgentExecutor, Options{})
	before := pool.Stat().AcquireCount()
	commitsBefore := trace.commits.Load()
	requireWorkerClaim(t, ctx, worker, "model/tool chain")
	acquires := pool.Stat().AcquireCount() - before
	commits := trace.commits.Load() - commitsBefore
	t.Logf("three model calls and two tools: %d pool acquisitions, %d commits", acquires, commits)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
	assertCompletedToolCallCount(t, ctx, pool, agentID, "list_agents", "completed", 2)
	require.LessOrEqual(t, acquires, int64(30))
	require.LessOrEqual(t, commits, int64(12))
}

type stepCommitTracer struct{ commits atomic.Int64 }

func (*stepCommitTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (t *stepCommitTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && data.CommandTag.String() == "COMMIT" {
		t.commits.Add(1)
	}
}

type credentialResolvingTestModel struct {
	resolver modelprovider.Resolver
	client   model.Client
}

func (r credentialResolvingTestModel) Resolve(
	ctx context.Context, selection model.Selection,
) (model.ResolvedClient, error) {
	resolved, err := r.resolver.Resolve(ctx, selection)
	if err != nil {
		return model.ResolvedClient{}, err
	}
	resolved.Client = r.client
	return resolved, nil
}

func TestWorkerFusedContinuationYieldsBeforePreparingOverBudgetModel(t *testing.T) {
	ctx := t.Context()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	agentID, userID := createWorkerAgentWithTools(t, ctx, store, time.Now(), "list_agents")
	createWorkerInput(t, ctx, store, agentID, userID, "continue", time.Now())
	executor := newObservedContinuationExecutor(store)
	client := &sequenceModelClient{providerModelSlug: "worker-kernel-test", responses: []model.Response{
		continuationToolResponse(1), continuationToolResponse(2), continuationToolResponse(3), continuationFinalResponse(4),
	}}
	executor.ModelResolver = liveWorkerTestModelResolver(store, client)
	worker := NewWorker(store.Execution(), executor.AgentExecutor, Options{})
	requireWorkerClaim(t, ctx, worker, "bounded fused chain")
	require.Equal(t, 3, client.calls)
	assertCompletedToolCallCount(t, ctx, pool, agentID, "list_agents", "completed", 3)
	waitNoRuntimeLock(t, ctx, pool, agentID)
	var contexts int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1`, agentID,
	).Scan(&contexts))
	require.Equal(t, 3, contexts, "yielding must not prepare an unused fourth attempt")
	requireWorkerClaim(t, ctx, worker, "reclaim yielded chain")
	require.Equal(t, 4, client.calls)
	waitNoWorkerWakeup(t, ctx, pool, agentID)
	waitNoRuntimeLock(t, ctx, pool, agentID)
}
