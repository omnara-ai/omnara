package storeutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/notifications"
)

const maxTransactionAttempts = 3

// RetryTransaction requires run to own the complete transaction and avoid irreversible work before commit.
func RetryTransaction[T any](ctx context.Context, run func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= maxTransactionAttempts; attempt++ {
		result, err := run()
		if err == nil || !retryableTransactionError(err) {
			return result, err
		}
		lastErr = err
		if attempt == maxTransactionAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, lastErr
}

func retryableTransactionError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func CommitTxWithNotifications(
	ctx context.Context,
	tx interface{ Commit(context.Context) error },
	txNotifications *notifications.TxNotifications,
	publisher notifications.PostCommitPublisher,
	operation string,
) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", operation, err)
	}
	txNotifications.Flush(context.WithoutCancel(ctx), publisher)
	return nil
}
