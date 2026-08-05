package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) EndExpiredDaemonRuntimes(
	ctx context.Context,
	batchLimit int32,
) ([]DaemonRuntimeRecord, error) {
	if batchLimit <= 0 {
		batchLimit = 100
	}
	candidates, err := s.q.ListExpiredDaemonRuntimeCandidates(
		ctx,
		dbsqlc.ListExpiredDaemonRuntimeCandidatesParams{BatchLimit: batchLimit},
	)
	if err != nil {
		return nil, fmt.Errorf("list expired daemon runtimes: %w", err)
	}
	records := make([]DaemonRuntimeRecord, 0, len(candidates))
	for _, candidate := range candidates {
		record, ended, err := s.endExpiredDaemonRuntime(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if ended {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *Store) endExpiredDaemonRuntime(
	ctx context.Context,
	candidate dbsqlc.ListExpiredDaemonRuntimeCandidatesRow,
) (DaemonRuntimeRecord, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonRuntimeRecord{}, false, fmt.Errorf("begin end expired daemon runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txNotifications := s.newTxNotifications()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: candidate.OrgID,
			ID:    candidate.MachineID,
		},
	); err != nil {
		return DaemonRuntimeRecord{}, false, fmt.Errorf(
			"lock machine for expired daemon runtime: %w",
			err,
		)
	}
	row, err := qtx.EndExpiredDaemonRuntime(
		ctx,
		dbsqlc.EndExpiredDaemonRuntimeParams(candidate),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"skipped expired daemon runtime",
		); err != nil {
			return DaemonRuntimeRecord{}, false, err
		}
		return DaemonRuntimeRecord{}, false, nil
	}
	if err != nil {
		return DaemonRuntimeRecord{}, false, fmt.Errorf("end expired daemon runtime: %w", err)
	}
	record := daemonRuntimeFromExpired(row)
	txNotifications.AddDaemonRuntimeEnded(
		record.ID,
		record.MachineID,
		notifications.DaemonRuntimeEndReconnect,
	)
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"end expired daemon runtime",
	); err != nil {
		return DaemonRuntimeRecord{}, false, err
	}
	return record, true, nil
}
