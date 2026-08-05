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

func Open(ctx context.Context, databaseURL string, opts ...OpenOption) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
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
