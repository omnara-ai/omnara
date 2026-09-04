package integrationdb

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AsyncResult[T any] struct {
	Value T
	Err   error
}

func BeginTx(t testing.TB, ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin integration test transaction: %v", err)
	}
	rollbackCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = tx.Rollback(rollbackCtx) })
	return tx
}

func RunAsync[T any](operation func() (T, error)) <-chan AsyncResult[T] {
	done := make(chan AsyncResult[T], 1)
	go func() {
		value, err := operation()
		done <- AsyncResult[T]{Value: value, Err: err}
	}()
	return done
}

func RunAsyncError(operation func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- operation() }()
	return done
}

func Await[T any](t testing.TB, done <-chan T, description string) T {
	t.Helper()
	timer := time.NewTimer(lockWaitTimeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func AwaitSuccess[T any](t testing.TB, done <-chan AsyncResult[T], description string) T {
	t.Helper()
	result := Await(t, done, description)
	if result.Err != nil {
		t.Fatalf("%s: %v", description, result.Err)
	}
	return result.Value
}
