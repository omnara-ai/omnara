package executionstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type EventWebhookBacklog struct {
	Due, Delayed, InFlight, Expired int64
	OldestDueAge, OldestDueLag      time.Duration
}

func (s *Store) SampleEventWebhookBacklog(ctx context.Context) (EventWebhookBacklog, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return EventWebhookBacklog{}, fmt.Errorf("sample event webhook backlog: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '3s'"); err != nil {
		return EventWebhookBacklog{}, fmt.Errorf("sample event webhook backlog: %w", err)
	}
	row, err := dbsqlc.New(tx).SampleEventWebhookBacklog(ctx, dbsqlc.SampleEventWebhookBacklogParams{
		DeliveryWindowSeconds: eventWebhookDeliveryWindow.Seconds(),
	})
	if err != nil {
		return EventWebhookBacklog{}, fmt.Errorf("sample event webhook backlog: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return EventWebhookBacklog{}, fmt.Errorf("sample event webhook backlog: %w", err)
	}
	return EventWebhookBacklog{
		Due: row.DueCount, Delayed: row.DelayedCount, InFlight: row.InFlightCount, Expired: row.ExpiredCount,
		OldestDueAge: time.Duration(row.OldestDueAgeSeconds * float64(time.Second)),
		OldestDueLag: time.Duration(row.OldestDueLagSeconds * float64(time.Second)),
	}, nil
}
