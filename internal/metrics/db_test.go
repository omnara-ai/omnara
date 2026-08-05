package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	if got, _ := record["db.queries.1.error"].(string); !strings.Contains(got, "duplicate key") {
		t.Fatalf("db.queries.1.error = %v, want duplicate key", record["db.queries.1.error"])
	}
	if got := record["db.queries.1.error_kind"]; got != "postgres" {
		t.Fatalf("db.queries.1.error_kind = %v, want postgres", got)
	}
	if got := record["db.queries.1.error_severity"]; got != "error" {
		t.Fatalf("db.queries.1.error_severity = %v, want error", got)
	}
	assertJSONNumber(t, record, "db.queries.error_count", 1)
	assertJSONNumber(t, record, "db.queries.duration_ms_sum", 20)
}

func TestDBRecorderScrubsTraceErrors(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	set := New()
	recorder := NewDBRecorder(set, SubsystemDB)
	ctx := log.WithLogger(context.Background(), logger)
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	traceCtx := recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "-- name: Connect :exec\nSELECT 1",
	})
	recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{
		Err: &pgconn.PgError{
			SeverityUnlocalized: "ERROR",
			Message:             "connect postgres://user:pass@db.example/app?sslpassword=secret failed token=tok_123",
		},
	})
	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	got, _ := record["db.queries.0.error"].(string)
	for _, forbidden := range []string{"user:pass@", "sslpassword=secret", "tok_123"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("db trace error leaked %q in %q", forbidden, got)
		}
	}
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
