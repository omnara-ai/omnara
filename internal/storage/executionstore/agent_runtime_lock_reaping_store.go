package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const maximumReportedAgentRuntimeLockReapErrors = 10

func (s *Store) ReapExpiredAgentRuntimeLocks(ctx context.Context, batchSize int32) (int64, error) {
	if batchSize <= 0 {
		return 0, errors.New("runtime lock reap batch size must be positive")
	}
	candidates, err := s.q.ListExpiredAgentRuntimeLockCandidates(
		ctx,
		dbsqlc.ListExpiredAgentRuntimeLockCandidatesParams{BatchSize: batchSize},
	)
	if err != nil {
		return 0, fmt.Errorf("list expired agent runtime locks: %w", err)
	}
	var total int64
	var reapErrs []error
	var suppressedErrors int
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		reaped, err := s.reapExpiredAgentRuntimeLock(
			ctx,
			candidate.ProjectID,
			candidate.AgentID,
			candidate.ID,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errutil.OnlyMatches(err, ctxErr) {
				break
			}
			err = fmt.Errorf(
				"reap runtime lock %s for agent %s in project %s: %w",
				candidate.ID,
				candidate.AgentID,
				candidate.ProjectID,
				err,
			)
			if len(reapErrs) < maximumReportedAgentRuntimeLockReapErrors {
				reapErrs = append(reapErrs, err)
			} else {
				suppressedErrors++
			}
			continue
		}
		if reaped {
			total++
		}
	}
	if suppressedErrors > 0 {
		reapErrs = append(reapErrs, fmt.Errorf(
			"%d additional runtime lock reap errors omitted",
			suppressedErrors,
		))
	}
	if len(reapErrs) > 0 {
		return total, errors.Join(reapErrs...)
	}
	return total, ctx.Err()
}

func (s *Store) reapExpiredAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID ID,
) (bool, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin reap expired agent runtime lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	reaped, err := reapExpiredAgentRuntimeLockTx(
		ctx,
		txNotifications,
		tx,
		projectID,
		agentID,
		runtimeLockID,
		s.modelCallRetryDelay,
	)
	if err != nil || !reaped {
		return false, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "reap expired agent runtime lock"); err != nil {
		return false, err
	}
	return true, nil
}

func reapExpiredAgentRuntimeLockTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	projectID, agentID, runtimeLockID ID,
	retryBackoff func(int, string) time.Duration,
) (bool, error) {
	qtx := dbsqlc.New(tx)
	_, err := qtx.TryLockAgentForRuntimeLockReap(
		ctx,
		dbsqlc.TryLockAgentForRuntimeLockReapParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock agent for expired runtime lock reap: %w", err)
	}
	locked, err := qtx.LockExpiredAgentRuntimeLockForReap(
		ctx,
		dbsqlc.LockExpiredAgentRuntimeLockForReapParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        runtimeLockID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock expired agent runtime lock for reap: %w", err)
	}
	if err := recoverRuntimeModelCallContextTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		locked.ProjectID,
		locked.AgentID,
		locked.ID,
		runtimeModelCallRecoveryEvidence{
			Code:    "runtime_lease_expired_before_model_result_acceptance",
			Message: "runtime lease expired before the model result was durably accepted",
		},
		retryBackoff,
	); err != nil {
		return false, err
	}
	if err := failRuntimeToolCallsTx(
		ctx,
		txNotifications,
		tx,
		locked.ProjectID,
		locked.AgentID,
		locked.ID,
		"runtime_lock_stale",
	); err != nil {
		return false, err
	}
	if err := failQueuedRuntimeWorkTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		locked.ProjectID,
		locked.AgentID,
		locked.ID,
		"runtime_lock_stale",
	); err != nil {
		return false, err
	}
	changed, err := qtx.DeleteAgentRuntimeLockForReap(
		ctx,
		dbsqlc.DeleteAgentRuntimeLockForReapParams{
			ProjectID: locked.ProjectID,
			AgentID:   locked.AgentID,
			ID:        locked.ID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("delete expired agent runtime lock: %w", err)
	}
	if changed != 1 {
		return false, storeerr.ErrRuntimeLockInactive
	}
	if err := qtx.ReconcileAgentWakeup(
		ctx,
		dbsqlc.ReconcileAgentWakeupParams{
			ProjectID: locked.ProjectID,
			AgentID:   locked.AgentID,
			Metadata:  []byte(`{"reason":"runtime_lock_reap"}`),
		},
	); err != nil {
		return false, fmt.Errorf("reconcile wakeup after expired runtime lock reap: %w", err)
	}
	return true, nil
}
