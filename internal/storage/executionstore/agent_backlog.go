package executionstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ReadyAgentBacklog struct {
	Ready          int64
	OldestReadyAge time.Duration
}

// SampleReadyAgentBacklog counts claimable agent wakeups on the pool it was
// opened with; open the store on the writer for scheduler-accurate results.
func (s *Store) SampleReadyAgentBacklog(ctx context.Context) (ReadyAgentBacklog, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return ReadyAgentBacklog{}, fmt.Errorf("sample ready agent backlog: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '3s'"); err != nil {
		return ReadyAgentBacklog{}, fmt.Errorf("sample ready agent backlog: %w", err)
	}
	row, err := dbsqlc.New(tx).SampleReadyAgentBacklog(ctx)
	if err != nil {
		return ReadyAgentBacklog{}, fmt.Errorf("sample ready agent backlog: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return ReadyAgentBacklog{}, fmt.Errorf("sample ready agent backlog: %w", err)
	}
	return ReadyAgentBacklog{
		Ready:          row.ReadyCount,
		OldestReadyAge: time.Duration(row.OldestReadyAgeSeconds * float64(time.Second)),
	}, nil
}
