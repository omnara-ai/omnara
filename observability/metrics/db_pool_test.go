package metrics

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestDBPoolCollectorScrapesWithoutConnecting(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://localhost/unused?pool_max_conns=3&pool_min_conns=0")
	require.NoError(t, err)
	var connects atomic.Int64
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		connects.Add(1)
		return errors.New("unexpected database connection")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	set := New()
	set.MustRegister(NewDBPoolCollector(pool))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = pool.Acquire(ctx)
	require.ErrorIs(t, err, context.Canceled)
	for range 2 {
		body := scrapeMetrics(t, set)
		require.Contains(t, body, "omnara_db_pool_max_connections 3")
		for _, name := range []string{"acquired_connections", "idle_connections", "constructing_connections",
			"acquires_total", "acquire_duration_seconds_total", "empty_acquires_total", "empty_acquire_wait_seconds_total"} {
			require.Contains(t, body, "omnara_db_pool_"+name+" 0")
		}
		require.Contains(t, body, "omnara_db_pool_canceled_acquires_total 1")
	}
	require.Zero(t, connects.Load(), "scrapes only read in-memory pool statistics")
}
