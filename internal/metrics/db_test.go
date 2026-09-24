package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/omnara-ai/omnara/internal/log"
)

func TestDBQueryName(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "sqlc query",
			sql:  "-- name: GetAgent :one\nSELECT id FROM agents",
			want: "GetAgent",
		},
		{
			name: "leading whitespace",
			sql:  "\n\t-- name: InsertAgent :one\nINSERT INTO agents",
			want: "InsertAgent",
		},
		{
			name: "plain sql",
			sql:  "SELECT 1",
			want: "unknown",
		},
		{
			name: "empty",
			sql:  "",
			want: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dbQueryName(tt.sql); got != tt.want {
				t.Fatalf("dbQueryName()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestDBRecorder(t *testing.T) {
	set := New()
	recorder := NewDBRecorder(set, SubsystemDB)
	now := time.Unix(100, 0)
	recorder.now = func() time.Time {
		return now
	}

	ctx := recorder.TraceQueryStart(
		context.Background(),
		nil,
		pgx.TraceQueryStartData{SQL: "-- name: GetAgent :one\nSELECT id FROM agents"},
	)
	now = now.Add(25 * time.Millisecond)
	recorder.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	ctx = recorder.TraceQueryStart(
		context.Background(),
		nil,
		pgx.TraceQueryStartData{SQL: "-- name: InsertAgent :one\nINSERT INTO agents"},
	)
	now = now.Add(10 * time.Millisecond)
	recorder.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Severity: "FATAL"}})

	req := httptest.NewRequest(http.MethodGet, ScrapePath, nil)
	resp := httptest.NewRecorder()
	set.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("scrape status=%d want=%d", resp.Code, http.StatusOK)
	}
	body := resp.Body.String()
	for _, want := range []string{
		`omnara_db_queries_total{error_kind="none",error_severity="none",query_name="GetAgent",result="success"} 1`,
		`omnara_db_queries_total{error_kind="postgres",error_severity="fatal",query_name="InsertAgent",result="error"} 1`,
		`omnara_db_query_duration_seconds_bucket{error_kind="none",error_severity="none",` +
			`query_name="GetAgent",result="success",le="0.025"} 1`,
		`omnara_db_query_duration_seconds_count{error_kind="postgres",error_severity="fatal",` +
			`query_name="InsertAgent",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestDBRecorderAttachesWideEventQueryData(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	set := New()
	recorder := NewDBRecorder(set, SubsystemDB)
	ctx := log.WithLogger(context.Background(), logger)
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	now := time.Now()
	recorder.now = func() time.Time { return now }

	traceCtx := recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "-- name: GetAgent :one\nSELECT id FROM agents",
	})
	now = now.Add(15 * time.Millisecond)
	recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{
		CommandTag: pgconn.NewCommandTag("SELECT 1"),
	})

	traceCtx = recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "-- name: InsertAgent :exec\nINSERT INTO agents",
	})
	now = now.Add(5 * time.Millisecond)
	recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{
		CommandTag: pgconn.NewCommandTag("INSERT 0 0"),
		Err:        &pgconn.PgError{SeverityUnlocalized: "ERROR", Message: "duplicate key"},
	})

	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	assertJSONNumber(t, record, "db.queries.count", 2)
	if got := record["db.queries.0.name"]; got != "GetAgent" {
		t.Fatalf("db.queries.0.name = %v, want GetAgent", got)
	}
	assertJSONNumber(t, record, "db.queries.0.duration_ms", 15)
	if got := record["db.queries.1.error_kind"]; got != "postgres" {
		t.Fatalf("db.queries.1.error_kind = %v, want postgres", got)
	}
	if got := record["db.queries.1.error_severity"]; got != "error" {
		t.Fatalf("db.queries.1.error_severity = %v, want error", got)
	}
	assertJSONNumber(t, record, "db.queries.error_count", 1)
	assertJSONNumber(t, record, "db.queries.duration_ms_sum", 20)
}

func TestDBQueryResult(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantResult   string
		wantKind     string
		wantSeverity string
	}{
		{
			name:         "success",
			wantResult:   "success",
			wantKind:     "none",
			wantSeverity: "none",
		},
		{
			name:         "canceled",
			err:          context.Canceled,
			wantResult:   "error",
			wantKind:     "context_canceled",
			wantSeverity: "none",
		},
		{
			name:         "postgres error",
			err:          &pgconn.PgError{SeverityUnlocalized: "ERROR"},
			wantResult:   "error",
			wantKind:     "postgres",
			wantSeverity: "error",
		},
		{
			name:         "postgres fatal fallback",
			err:          &pgconn.PgError{Severity: "FATAL"},
			wantResult:   "error",
			wantKind:     "postgres",
			wantSeverity: "fatal",
		},
		{
			name:         "postgres unlocalized severity wins",
			err:          &pgconn.PgError{Severity: "localized", SeverityUnlocalized: "PANIC"},
			wantResult:   "error",
			wantKind:     "postgres",
			wantSeverity: "panic",
		},
		{
			name:         "postgres deadlock",
			err:          &pgconn.PgError{Code: "40P01", SeverityUnlocalized: "ERROR"},
			wantResult:   "error",
			wantKind:     "postgres_deadlock",
			wantSeverity: "error",
		},
		{
			name:         "postgres serialization failure",
			err:          &pgconn.PgError{Code: "40001", SeverityUnlocalized: "ERROR"},
			wantResult:   "error",
			wantKind:     "postgres_serialization_failure",
			wantSeverity: "error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotResult, gotKind, gotSeverity := dbQueryResult(tt.err)
			if gotResult != tt.wantResult || gotKind != tt.wantKind || gotSeverity != tt.wantSeverity {
				t.Fatalf(
					"dbQueryResult()=(%q, %q, %q) want=(%q, %q, %q)",
					gotResult,
					gotKind,
					gotSeverity,
					tt.wantResult,
					tt.wantKind,
					tt.wantSeverity,
				)
			}
		})
	}
}

func assertJSONNumber(t *testing.T, record map[string]any, key string, want int) {
	t.Helper()
	got, ok := record[key].(float64)
	if !ok || int(got) != want {
		t.Fatalf("%s = %v (%T), want %d", key, record[key], record[key], want)
	}
}

func TestDBRecorderStructuredErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		cause  error
		kind   string
		code   string
		source string
	}{
		{
			name:   "statement timeout",
			err:    &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
			kind:   "postgres_statement_timeout",
			code:   "57014",
			source: "postgres",
		},
		{
			name: "lock timeout",
			err:  &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
			kind: "postgres_lock_timeout",
			code: "55P03",
		},
		{
			name: "lock unavailable",
			err:  &pgconn.PgError{Code: "55P03", Message: "customer_content"},
			kind: "postgres_lock_not_available",
			code: "55P03",
		},
		{
			name:   "server cancellation",
			err:    &pgconn.PgError{Code: "57014", Message: "customer_content"},
			kind:   "postgres_query_canceled",
			code:   "57014",
			source: "postgres",
		},
		{
			name: "wrapped postgres",
			err: fmt.Errorf("customer_content: %w", &pgconn.PgError{
				Code:          "22P02",
				Message:       "customer_content",
				Detail:        "customer_content",
				InternalQuery: "customer_content",
			}),
			kind: "postgres",
			code: "22P02",
		},
		{
			name: "unknown error",
			err:  errors.New("customer_content"),
			kind: "other",
		},
		{
			name: "empty error",
			err:  errors.New(""),
			kind: "other",
		},
		{
			name:   "wrapped cancellation",
			err:    fmt.Errorf("customer_content: %w", context.Canceled),
			cause:  log.ErrSSEShutdown,
			kind:   "context_canceled",
			source: "sse_shutdown",
		},
		{
			name:   "server cancellation with local cause",
			err:    &pgconn.PgError{Code: "57014"},
			cause:  log.ErrSocketClosed,
			kind:   "postgres_query_canceled",
			code:   "57014",
			source: "socket_closed",
		},
		{
			name:  "unrelated failure during cancellation",
			err:   &pgconn.PgError{Code: "23505"},
			cause: log.ErrSSEShutdown,
			kind:  "postgres",
			code:  "23505",
		},
		{
			name:   "unknown cancellation",
			err:    context.Canceled,
			cause:  errors.New("customer_content"),
			kind:   "context_canceled",
			source: "unknown",
		},
		{
			name:  "success after cancellation",
			cause: log.ErrSSEShutdown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := log.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&buf, nil)))
			event := log.NewEvent(ctx, "test.event")
			ctx = log.WithEvent(ctx, event)
			if tt.cause != nil {
				child, cancel := context.WithCancelCause(ctx)
				cancel(tt.cause)
				ctx = child
			}
			recorder := NewDBRecorder(New(), SubsystemDB)
			traceCtx := recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
				SQL:  "-- name: TestQuery :one\nSELECT 'customer_content'",
				Args: []any{"customer_content"},
			})
			recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{Err: tt.err})
			event.Done(ctx)
			if strings.Contains(buf.String(), "customer_content") {
				t.Fatalf("customer content leaked: %s", buf.String())
			}
			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"error_kind": tt.kind, "sqlstate": tt.code, "cancel_source": tt.source} {
				got, _ := record["db.queries.0."+key].(string)
				if got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			wantCount := float64(0)
			if tt.err != nil {
				wantCount = 1
			}
			if record["db.queries.error_count"] != wantCount {
				t.Errorf("error_count = %v, want %v", record["db.queries.error_count"], wantCount)
			}
		})
	}
}

func TestDBRecorderWrappedWriteTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	if err := client.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	frontend := pgproto3.NewFrontend(nil, client)
	frontend.Send(&pgproto3.Query{String: "customer_content"})
	writeErr := frontend.Flush()
	var netErr net.Error
	if !errors.As(writeErr, &netErr) || !netErr.Timeout() || pgconn.Timeout(writeErr) {
		t.Fatalf("expected a network timeout outside pgconn's timeout wrapper, got %v", writeErr)
	}
	for _, tt := range []struct {
		name             string
		cancelRequest    bool
		cancelAfterTrace bool
		localCause       error
		source           string
	}{
		{name: "active request", source: "unknown"},
		{name: "canceled request", cancelRequest: true, source: "unknown"},
		{name: "later request cancellation", cancelAfterTrace: true, source: "unknown"},
		{name: "child canceled before request", localCause: context.Canceled, cancelRequest: true, source: "unknown"},
		{name: "stream before request", localCause: log.ErrSSEShutdown, cancelRequest: true, source: "sse_shutdown"},
		{name: "stream shutdown", localCause: log.ErrSSEShutdown, source: "sse_shutdown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			parent = log.WithLogger(parent, slog.New(slog.NewJSONHandler(&buf, nil)))
			request := httptest.NewRequestWithContext(parent, http.MethodGet, "/", nil)
			ctx, _, event := log.HTTPRequest(parent, httptest.NewRecorder(), request)
			if tt.localCause != nil {
				child, cancelChild := context.WithCancelCause(ctx)
				cancelChild(tt.localCause)
				ctx = child
			}
			recorder := NewDBRecorder(New(), SubsystemDB)
			traceCtx := recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
				SQL:  "-- name: AuthenticateBrowserSession :one\nSELECT 'customer_content'",
				Args: []any{"customer_content"},
			})
			if tt.cancelRequest {
				cancel()
			}
			recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{Err: writeErr})
			if tt.cancelAfterTrace {
				cancel()
			}
			event.Done(ctx)
			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"name": "AuthenticateBrowserSession", "error_kind": "driver_timeout",
				"error_severity": "unknown",
				"cancel_source":  tt.source,
			} {
				if got := record["db.queries.0."+key]; got != want {
					t.Errorf("%s = %v, want %q", key, got, want)
				}
			}
			if got := record["db.queries.0.request_canceled"]; got != tt.cancelRequest {
				t.Errorf("request_canceled = %v, want %v", got, tt.cancelRequest)
			}
			if _, ok := record["db.queries.0.sqlstate"]; ok {
				t.Error("network timeout must not have SQLSTATE")
			}
			assertJSONNumber(t, record, "db.queries.error_count", 1)
			if strings.Contains(buf.String(), "customer_content") || strings.Contains(buf.String(), writeErr.Error()) {
				t.Fatalf("raw query or error leaked: %s", buf.String())
			}
		})
	}
}

func TestDBQueryResultNonTimeoutNetworkError(t *testing.T) {
	err := fmt.Errorf("write failed: %w", &net.OpError{Op: "write", Net: "tcp", Err: io.ErrClosedPipe})
	result, kind, _ := dbQueryResult(err)
	if result != "error" || kind != "other" {
		t.Fatalf("result=%q kind=%q", result, kind)
	}
}
