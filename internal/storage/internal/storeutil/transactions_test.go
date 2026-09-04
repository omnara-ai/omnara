package storeutil_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type recordingCommitter struct {
	err      error
	order    *[]string
	afterRun func()
}

func TestRetryTransactionRetriesPostgresTransactionConflicts(t *testing.T) {
	for _, code := range []string{"40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			attempts := 0
			result, err := storeutil.RetryTransaction(context.Background(), func() (string, error) {
				attempts++
				if attempts < 3 {
					return "", &pgconn.PgError{Code: code}
				}
				return "committed", nil
			})
			if err != nil {
				t.Fatalf("RetryTransaction() error = %v", err)
			}
			if result != "committed" || attempts != 3 {
				t.Fatalf("result=%q attempts=%d", result, attempts)
			}
		})
	}
}

func TestRetryTransactionStopsOnNonRetryableError(t *testing.T) {
	wantErr := errors.New("invalid input")
	attempts := 0
	_, err := storeutil.RetryTransaction(context.Background(), func() (struct{}, error) {
		attempts++
		return struct{}{}, wantErr
	})
	if !errors.Is(err, wantErr) || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}

func TestRetryTransactionHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	_, err := storeutil.RetryTransaction(ctx, func() (struct{}, error) {
		attempts++
		return struct{}{}, &pgconn.PgError{Code: "40P01"}
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}

func (c recordingCommitter) Commit(context.Context) error {
	*c.order = append(*c.order, "commit")
	if c.afterRun != nil {
		c.afterRun()
	}
	return c.err
}

type recordingPublisher struct {
	order      *[]string
	contextErr error
	intents    []notifications.PostCommitIntent
}

func (p *recordingPublisher) PublishPostCommit(
	ctx context.Context,
	intent notifications.PostCommitIntent,
) {
	*p.order = append(*p.order, "publish")
	p.contextErr = ctx.Err()
	p.intents = append(p.intents, intent)
}

func TestCommitTxWithNotificationsCommitsBeforePublishing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	order := []string{}
	agentID := uuid.New()
	txNotifications := notifications.NewTxNotifications()
	txNotifications.AddAgentEvent(agentID)
	publisher := &recordingPublisher{order: &order}

	err := storeutil.CommitTxWithNotifications(
		ctx,
		recordingCommitter{order: &order, afterRun: cancel},
		txNotifications,
		publisher,
		"test operation",
	)
	if err != nil {
		t.Fatalf("CommitTxWithNotifications() error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"commit", "publish"}) {
		t.Fatalf("operation order = %v", order)
	}
	if publisher.contextErr != nil {
		t.Fatalf("publish context error = %v", publisher.contextErr)
	}
	if len(publisher.intents) != 1 {
		t.Fatalf("published intents = %d", len(publisher.intents))
	}
	intent, ok := publisher.intents[0].(notifications.AgentEventCommitted)
	if !ok || intent.AgentID != agentID {
		t.Fatalf("published intent = %#v", publisher.intents[0])
	}
}

func TestCommitTxWithNotificationsDoesNotPublishAfterCommitFailure(t *testing.T) {
	commitErr := errors.New("commit failed")
	order := []string{}
	txNotifications := notifications.NewTxNotifications()
	txNotifications.AddAgentEvent(uuid.New())
	publisher := &recordingPublisher{order: &order}

	err := storeutil.CommitTxWithNotifications(
		context.Background(),
		recordingCommitter{err: commitErr, order: &order},
		txNotifications,
		publisher,
		"test operation",
	)
	if !errors.Is(err, commitErr) {
		t.Fatalf("CommitTxWithNotifications() error = %v", err)
	}
	if err.Error() != "commit test operation: commit failed" {
		t.Fatalf("CommitTxWithNotifications() error = %q", err)
	}
	if !reflect.DeepEqual(order, []string{"commit"}) {
		t.Fatalf("operation order = %v", order)
	}
	if len(publisher.intents) != 0 {
		t.Fatalf("published intents = %d", len(publisher.intents))
	}
}
