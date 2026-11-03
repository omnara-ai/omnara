package storeutil_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func TestRetryTransactionRetriesPostgresTransactionConflicts(t *testing.T) {
	for _, code := range []string{"40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			attempts := 0
			result, err := storeutil.RetryTransaction(context.Background(), "test", func() (string, error) {
				attempts++
				if attempts < 3 {
					return "uncommitted", fmt.Errorf("attempt failed: %w", &pgconn.PgError{Code: code})
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
	for _, wantErr := range []error{
		errors.New("invalid input"), &pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "08006"},
	} {
		attempts := 0
		_, err := storeutil.RetryTransaction(context.Background(), "test", func() (struct{}, error) {
			attempts++
			return struct{}{}, wantErr
		})
		if !errors.Is(err, wantErr) || attempts != 1 {
			t.Fatalf("error=%v attempts=%d", err, attempts)
		}
	}
}

func TestRetryTransactionExhaustionDiscardsUncommittedResult(t *testing.T) {
	wantErr := &pgconn.PgError{Code: "40001"}
	attempts := 0
	result, err := storeutil.RetryTransaction(context.Background(), "test", func() (string, error) {
		attempts++
		return "uncommitted", wantErr
	})
	if !errors.Is(err, wantErr) || attempts != 3 || result != "" {
		t.Fatalf("result=%q error=%v attempts=%d", result, err, attempts)
	}
}

func TestRetryTransactionHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	_, err := storeutil.RetryTransaction(ctx, "test", func() (struct{}, error) {
		attempts++
		cancel()
		return struct{}{}, &pgconn.PgError{Code: "40P01"}
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}

func TestRetryTransactionDoesNotLogSuccessOnPanic(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	ctx := log.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	attempts := 0
	func() {
		defer func() {
			if got := recover(); got != "test panic" {
				t.Fatalf("panic = %v, want original panic", got)
			}
		}()
		_, _ = storeutil.RetryTransaction(ctx, "test", func() (struct{}, error) {
			attempts++
			if attempts == 1 {
				return struct{}{}, &pgconn.PgError{Code: "40P01"}
			}
			panic("test panic")
		})
	}()
	var summary struct {
		Attempts int    `json:"db.transaction.attempts"`
		Outcome  string `json:"db.transaction.outcome"`
	}
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatalf("decode retry summary: %v", err)
	}
	if summary.Attempts != 2 || summary.Outcome != "error" {
		t.Fatalf("panic retry summary = %+v, want two attempts and error", summary)
	}
}

func TestRetryTransactionLogsOneBoundedSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		codes        []string
		cancelOnLast bool
		outcome      string
	}{
		{"ordinary success", []string{""}, false, ""},
		{"ordinary error", []string{"23505"}, false, ""},
		{"recovery", []string{"40P01", "40001", ""}, false, "success"},
		{"exhaustion", []string{"40P01", "40P01", "40P01"}, false, "exhausted"},
		{"terminal error", []string{"40001", "23505"}, false, "error"},
		{"canceled backoff", []string{"40P01"}, true, "canceled"},
		{"success then canceled", []string{"40P01", ""}, true, "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			ctx, cancel := context.WithCancel(log.WithLogger(context.Background(),
				slog.New(slog.NewJSONHandler(&output, nil))))
			defer cancel()
			parent := log.NewEvent(ctx, "parent")
			ctx = log.WithEvent(ctx, parent)
			attempts := 0
			_, _ = storeutil.RetryTransaction(ctx, "begin_machine_wake", func() (struct{}, error) {
				code := tc.codes[attempts]
				attempts++
				if tc.cancelOnLast && attempts == len(tc.codes) {
					cancel()
				}
				if code == "" {
					return struct{}{}, nil
				}
				return struct{}{}, &pgconn.PgError{Code: code, Message: "private database error detail"}
			})
			if attempts != len(tc.codes) {
				t.Fatalf("attempts = %d, want %d", attempts, len(tc.codes))
			}
			if tc.outcome == "" {
				if output.Len() != 0 {
					t.Fatalf("transaction without conflicts logged %s", output.String())
				}
				return
			}
			if strings.Count(output.String(), "\n") != 1 || strings.Contains(output.String(), "private database") {
				t.Fatalf("want one summary without raw database error, got %s", output.String())
			}
			var summary struct {
				Operation string `json:"db.transaction.operation"`
				Attempts  int    `json:"db.transaction.attempts"`
				Outcome   string `json:"db.transaction.outcome"`
				Parent    string `json:"parent.event.name"`
				Conflicts []struct {
					Attempt  int    `json:"attempt"`
					SQLState string `json:"sqlstate"`
				} `json:"db.transaction.conflicts"`
			}
			if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
				t.Fatalf("decode retry summary: %v", err)
			}
			if summary.Operation != "begin_machine_wake" || summary.Attempts != attempts ||
				summary.Outcome != tc.outcome || summary.Parent != "parent" {
				t.Fatalf("unexpected retry summary: %+v", summary)
			}
			wantConflicts := 0
			for i, code := range tc.codes {
				if code != "40P01" && code != "40001" {
					continue
				}
				if wantConflicts >= len(summary.Conflicts) {
					t.Fatalf("missing conflict at attempt %d: %+v", i+1, summary)
				}
				conflict := summary.Conflicts[wantConflicts]
				if conflict.Attempt != i+1 || conflict.SQLState != code {
					t.Fatalf("unexpected conflict at attempt %d: %+v", i+1, conflict)
				}
				wantConflicts++
			}
			if len(summary.Conflicts) != wantConflicts {
				t.Fatalf("conflicts = %d, want %d", len(summary.Conflicts), wantConflicts)
			}
		})
	}
}
