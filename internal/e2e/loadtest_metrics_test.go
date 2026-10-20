//go:build integration && servicee2e && loadtest

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLoadExplicitTransactionRatesRejectDiscontinuousStatistics(t *testing.T) {
	reset := time.Now()
	start := &loadExplicitTransactions{begins: 100, commits: 80, resetAt: reset, dealloc: 2}
	end := &loadExplicitTransactions{begins: 150, commits: 120, resetAt: reset, dealloc: 2}
	begins, commits := loadExplicitTransactionRates(start, end, 10)
	require.NotNil(t, begins)
	require.NotNil(t, commits)
	require.Equal(t, 5.0, *begins)
	require.Equal(t, 4.0, *commits)
	for _, changed := range []*loadExplicitTransactions{
		nil,
		{begins: 150, commits: 120, resetAt: reset.Add(time.Second), dealloc: 2},
		{begins: 150, commits: 120, resetAt: reset, dealloc: 3},
		{begins: 90, commits: 120, resetAt: reset, dealloc: 2},
		{begins: 150, commits: 70, resetAt: reset, dealloc: 2},
	} {
		begins, commits := loadExplicitTransactionRates(start, changed, 10)
		require.Nil(t, begins)
		require.Nil(t, commits)
	}
}

func TestLoadProviderRequestsUseMeasurementWindow(t *testing.T) {
	observer := newProviderObserver()
	start := time.Now()
	observer.start("agent", start.Add(-time.Second))
	observer.start("agent", start)
	observer.start("agent", start.Add(time.Second))
	observer.start("agent", start.Add(2*time.Second))
	require.Equal(t, int64(2), observer.requestsInWindow(start, start.Add(2*time.Second)))
}

func TestLoadTransactionMeasurementsExcludeImplicitReads(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	_, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
	require.NoError(t, err)
	env := serviceE2EEnvironment{db: pool}
	start, err := env.loadExplicitTransactions(ctx)
	require.NoError(t, err)
	for _, commit := range []bool{true, false} {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		if commit {
			require.NoError(t, tx.Commit(ctx))
		} else {
			require.NoError(t, tx.Rollback(ctx))
		}
	}
	for range 3 {
		_, err := pool.Exec(ctx, "SELECT 1")
		require.NoError(t, err)
	}
	end, err := env.loadExplicitTransactions(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), end.begins-start.begins)
	require.Equal(t, int64(1), end.commits-start.commits)
	_, err = env.loadPGStat(ctx)
	require.NoError(t, err)
}

func TestLoadTurnMetricsUseMeasurementWindow(t *testing.T) {
	start := time.Now()
	end := start.Add(10 * time.Second)
	samples := []loadTurnSample{
		{at: start.Add(-time.Nanosecond), duration: time.Hour},
		{at: start, duration: 3 * time.Second},
		{at: end.Add(-time.Nanosecond), duration: time.Second},
		{at: end, duration: 2 * time.Hour},
		{at: end.Add(time.Second), duration: 3 * time.Hour},
	}
	times := loadTurnTimes(samples, start, end)
	require.Equal(t, []float64{1, 3}, times)
	require.Equal(t, 0.2, float64(len(times))/end.Sub(start).Seconds())
	require.Empty(t, loadTurnTimes(samples, end, end))
}

func TestLoadQueryDeltasUsePerQueryWindowCounters(t *testing.T) {
	reset := time.Now()
	key := loadQueryKey{userID: 1, queryID: 42, toplevel: true}
	otherUser := loadQueryKey{userID: 2, queryID: 42, toplevel: true}
	nested := loadQueryKey{userID: 1, queryID: 42}
	newQuery := loadQueryKey{userID: 1, queryID: 43, toplevel: true}
	start := &loadQuerySnapshot{resetAt: reset, queries: map[loadQueryKey]loadQueryStat{
		key:       {calls: 100, totalMS: 10000, name: "warmup"},
		otherUser: {calls: 10, totalMS: 50, name: "other user"},
		nested:    {calls: 2, totalMS: 20, name: "nested"},
	}}
	end := &loadQuerySnapshot{resetAt: reset, queries: map[loadQueryKey]loadQueryStat{
		key:       {calls: 102, totalMS: 10030, name: "warmup"},
		otherUser: {calls: 10, totalMS: 50, name: "other user"},
		nested:    {calls: 3, totalMS: 25, name: "nested"},
		newQuery:  {calls: 3, totalMS: 60, name: "new query"},
	}}
	deltas, ok := loadQueryDeltas(start, end)
	require.True(t, ok)
	require.Equal(t, []loadQueryStat{
		{calls: 3, totalMS: 60, name: "new query"},
		{calls: 2, totalMS: 30, name: "warmup"},
		{calls: 1, totalMS: 5, name: "nested"},
	}, deltas)
	require.Equal(t, int64(100), start.queries[key].calls)
	require.Equal(t, int64(102), end.queries[key].calls)
}

func TestLoadQueryDeltasRejectDiscontinuousStatistics(t *testing.T) {
	reset := time.Now()
	key := loadQueryKey{userID: 1, queryID: 42, toplevel: true}
	start := &loadQuerySnapshot{resetAt: reset, dealloc: 2, queries: map[loadQueryKey]loadQueryStat{
		key: {calls: 100, totalMS: 1000},
	}}
	for _, end := range []*loadQuerySnapshot{
		nil,
		{resetAt: reset.Add(time.Second), dealloc: 2, queries: start.queries},
		{resetAt: reset, dealloc: 3, queries: start.queries},
		{resetAt: reset, dealloc: 2, queries: map[loadQueryKey]loadQueryStat{}},
		{resetAt: reset, dealloc: 2, queries: map[loadQueryKey]loadQueryStat{key: {calls: 99, totalMS: 1100}}},
		{resetAt: reset, dealloc: 2, queries: map[loadQueryKey]loadQueryStat{key: {calls: 101, totalMS: 999}}},
	} {
		deltas, ok := loadQueryDeltas(start, end)
		require.False(t, ok)
		require.Nil(t, deltas)
	}
	_, ok := loadQueryDeltas(nil, start)
	require.False(t, ok)
}

func TestLoadQuerySnapshotsPreserveCumulativeStatistics(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	_, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
	require.NoError(t, err)
	env := serviceE2EEnvironment{db: pool}
	for range 2 {
		_, err := pool.Exec(ctx, "SELECT pg_sleep(0)")
		require.NoError(t, err)
	}
	start, err := env.loadQuerySnapshot(ctx)
	require.NoError(t, err)
	for range 3 {
		_, err := pool.Exec(ctx, "SELECT pg_sleep(0)")
		require.NoError(t, err)
	}
	end, err := env.loadQuerySnapshot(ctx)
	require.NoError(t, err)
	deltas, ok := loadQueryDeltas(start, end)
	require.True(t, ok)
	found := false
	for key, stat := range end.queries {
		if strings.HasPrefix(stat.name, "SELECT pg_sleep(") {
			found = true
			require.Equal(t, int64(2), start.queries[key].calls)
			require.Equal(t, int64(5), stat.calls)
		}
	}
	require.True(t, found)
	found = false
	for _, stat := range deltas {
		if strings.HasPrefix(stat.name, "SELECT pg_sleep(") {
			found = true
			require.Equal(t, int64(3), stat.calls)
			require.GreaterOrEqual(t, stat.totalMS, 0.0)
		}
	}
	require.True(t, found)
}
