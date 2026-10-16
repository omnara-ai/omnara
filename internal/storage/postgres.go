package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DefaultMaxConns = 10

type OpenOption func(*pgxpool.Config, map[string]string)

func WithQueryTracer(tracer pgx.QueryTracer) OpenOption {
	return func(cfg *pgxpool.Config, _ map[string]string) {
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

func WithDefaultApplicationName(name string) OpenOption {
	return func(cfg *pgxpool.Config, _ map[string]string) {
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

func WithDefaultMaxConns(maxConns int32) OpenOption {
	return func(cfg *pgxpool.Config, explicit map[string]string) {
		if _, ok := explicit["pool_max_conns"]; !ok {
			cfg.MaxConns = maxConns
		}
	}
}

func Open(ctx context.Context, databaseURL string, opts ...OpenOption) (*pgxpool.Pool, error) {
	cfg, err := parsePoolConfig(databaseURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
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

func parsePoolConfig(databaseURL string, opts ...OpenOption) (*pgxpool.Config, error) {
	// pgxpool strips pool_* settings; raw parsing preserves whether they were explicit.
	raw, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if _, ok := raw.Config.RuntimeParams["pool_max_conns"]; !ok {
		cfg.MaxConns = DefaultMaxConns
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
	for _, opt := range opts {
		opt(cfg, raw.Config.RuntimeParams)
	}
	return cfg, nil
}
