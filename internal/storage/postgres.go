package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OpenOption func(*pgxpool.Config)

func WithQueryTracer(tracer pgx.QueryTracer) OpenOption {
	return func(cfg *pgxpool.Config) {
		if tracer == nil {
			return
		}
		if cfg.ConnConfig.Tracer == nil {
			cfg.ConnConfig.Tracer = tracer
			return
		}
		cfg.ConnConfig.Tracer = multitracer.New(cfg.ConnConfig.Tracer, tracer)
	}
}

// WithDefaultApplicationName sets the process name reported by PostgreSQL only
// when the connection string or PostgreSQL environment did not provide one.
func WithDefaultApplicationName(name string) OpenOption {
	return func(cfg *pgxpool.Config) {
		if name == "" {
			return
		}
		if cfg.ConnConfig.Config.RuntimeParams == nil {
			cfg.ConnConfig.Config.RuntimeParams = make(map[string]string)
		}
		if _, ok := cfg.ConnConfig.Config.RuntimeParams["application_name"]; !ok {
			cfg.ConnConfig.Config.RuntimeParams["application_name"] = name
		}
	}
}

func Open(ctx context.Context, databaseURL string, opts ...OpenOption) (*pgxpool.Pool, error) {
	cfg, err := parsePoolConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	for _, opt := range opts {
		opt(cfg)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func parsePoolConfig(databaseURL string) (*pgxpool.Config, error) {
	// pgxpool removes pool_* settings after parsing them. Parse once with pgx so
	// we can distinguish an explicit setting (including environment/service-file
	// input) from pgxpool's own default before applying Omnara's defaults.
	raw, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if _, ok := raw.Config.RuntimeParams["pool_max_conns"]; !ok {
		cfg.MaxConns = 10
	}
	if _, ok := raw.Config.RuntimeParams["pool_min_conns"]; !ok {
		cfg.MinConns = 1
	}
	if _, ok := raw.Config.RuntimeParams["pool_max_conn_lifetime"]; !ok {
		cfg.MaxConnLifetime = 30 * time.Minute
	}
	if _, ok := raw.Config.RuntimeParams["pool_max_conn_idle_time"]; !ok {
		cfg.MaxConnIdleTime = 30 * time.Minute
	}
	if _, ok := raw.Config.RuntimeParams["pool_max_conn_lifetime_jitter"]; !ok {
		cfg.MaxConnLifetimeJitter = 5 * time.Minute
	}
	return cfg, nil
}
