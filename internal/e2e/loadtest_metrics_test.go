//go:build integration && servicee2e && loadtest

package e2e

import (
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
