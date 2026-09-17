package storeutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/notifications"
)

const maxTransactionAttempts = 3

var ErrRetryTransaction = errors.New("transaction must be retried")

// RetryTransaction requires run to own the complete transaction and avoid irreversible work before commit.
func RetryTransaction[T any](ctx context.Context, operation string, run func() (T, error)) (out T, finalErr error) {
	var event *log.Event
	var conflicts []transactionConflict
	var attempt int
	var returned bool
	defer func() {
		if event == nil {
			return
		}
		outcome := "error"
		switch {
		case returned && finalErr == nil:
			outcome = "success"
		case errors.Is(finalErr, context.Canceled), errors.Is(finalErr, context.DeadlineExceeded):
			outcome = "canceled"
		case len(conflicts) == maxTransactionAttempts:
			outcome = "exhausted"
		}
		event.Attach(log.Fields{
			"db.transaction.attempts":  attempt,
			"db.transaction.conflicts": conflicts,
			"db.transaction.outcome":   outcome,
		})
		level := log.WarnLevel
		if outcome == "error" || outcome == "exhausted" {
			level = log.ErrorLevel
		}
		event.Level(level)
		event.Done(ctx)
	}()
	var lastErr error
	for attempt = 1; attempt <= maxTransactionAttempts; attempt++ {
		returned = false
		result, err := run()
		returned = true
		code := retryableTransactionSQLState(err)
		if code == "" {
			return result, err
		}
		if event == nil {
			event = log.NewEvent(ctx, "db.transaction.retry", log.Fields{"db.transaction.operation": operation})
		}
		conflicts = append(conflicts, transactionConflict{Attempt: attempt, SQLState: code})
		lastErr = err
		if attempt == maxTransactionAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
	return out, lastErr
}

type transactionConflict struct {
	Attempt  int    `json:"attempt"`
	SQLState string `json:"sqlstate"`
}

func retryableTransactionSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
		return pgErr.Code
	}
	if errors.Is(err, ErrRetryTransaction) {
		return "retry"
	}
	return ""
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
