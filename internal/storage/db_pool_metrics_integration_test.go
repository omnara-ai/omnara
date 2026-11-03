//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestDBPoolCollectorWaitAndUtilization(t *testing.T) {
	base := integrationdb.OpenUnmigratedPool(t, t.Context())
	cfg := base.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics.NewDBPoolCollector(pool))
	value := func(name string) float64 {
		t.Helper()
		families, err := registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() != "omnara_db_pool_"+name {
				continue
			}
			require.Len(t, family.Metric, 1)
			require.Empty(t, family.Metric[0].Label)
			if family.GetType() == dto.MetricType_COUNTER {
				return family.Metric[0].GetCounter().GetValue()
			}
			return family.Metric[0].GetGauge().GetValue()
		}
		t.Fatalf("metric %s missing", name)
		return 0
	}
	held, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	defer held.Release()
	require.Equal(t, 1.0, value("max_connections"))
	require.Equal(t, 1.0, value("acquired_connections"))
	require.Zero(t, value("idle_connections"))
	require.Positive(t, value("empty_acquires_total"))
	require.Positive(t, value("empty_acquire_wait_seconds_total"))

	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = pool.Acquire(waitCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1.0, value("canceled_acquires_total"))
	require.Equal(t, 1.0, value("acquires_total"))

	held.Release()
	conn, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	conn.Release()
	require.Equal(t, 2.0, value("acquires_total"))
	require.GreaterOrEqual(t, value("acquire_duration_seconds_total"), value("empty_acquire_wait_seconds_total"))
	require.Zero(t, value("acquired_connections"))
	require.Equal(t, 1.0, value("idle_connections"))
}
