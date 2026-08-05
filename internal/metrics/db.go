package metrics

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/omnara-ai/omnara/internal/log"
)

const (
	SubsystemDB = "db"
)

type dbQueryContextKey struct{}

type dbQueryTrace struct {
	queryName string
	start     time.Time
}

type DBRecorder struct {
	queriesTotal  *prometheus.CounterVec
	queryDuration *prometheus.HistogramVec
	now           func() time.Time
}

func NewDBRecorder(set *Set, subsystem string) *DBRecorder {
	m := &DBRecorder{
		queriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: subsystem,
			Name:      "queries_total",
			Help:      "Total number of database queries.",
		}, []string{"query_name", "result", "error_kind", "error_severity"}),
		queryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omnara",
			Subsystem: subsystem,
			Name:      "query_duration_seconds",
			Help:      "Database query duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"query_name", "result", "error_kind", "error_severity"}),
		now: time.Now,
	}
	set.MustRegister(m.queriesTotal, m.queryDuration)
	return m
}

func (m *DBRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	now := m.now()
	return context.WithValue(ctx, dbQueryContextKey{}, dbQueryTrace{
		queryName: dbQueryName(data.SQL),
		start:     now,
	})
}

func (m *DBRecorder) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(dbQueryContextKey{}).(dbQueryTrace)
	if !ok || trace.start.IsZero() {
		return
	}
	result, errorKind, errorSeverity := dbQueryResult(data.Err)
	duration := m.now().Sub(trace.start)
	rows := data.CommandTag.RowsAffected()
	var errText string
	if data.Err != nil {
		errText = truncate(log.ScrubLogString(data.Err.Error()), 256)
	}
	log.AttachDBQuery(ctx, log.DBQueryTraceRecord{
		Name:          trace.queryName,
		Start:         trace.start,
		Duration:      duration,
		Rows:          rows,
		Error:         errText,
		ErrorKind:     errorKind,
		ErrorSeverity: errorSeverity,
	})
	labels := []string{trace.queryName, result, errorKind, errorSeverity}
	m.queriesTotal.WithLabelValues(labels...).Inc()
	m.queryDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func dbQueryName(sql string) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return "unknown"
	}
	firstLine, _, _ := strings.Cut(sql, "\n")
	firstLine = strings.TrimSpace(firstLine)
	const prefix = "-- name:"
	if !strings.HasPrefix(firstLine, prefix) {
		return "unknown"
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(firstLine, prefix)))
	if len(fields) == 0 {
		return "unknown"
	}
	return fields[0]
}

func dbQueryResult(err error) (result string, errorKind string, errorSeverity string) {
	if err == nil {
		return "success", "none", "none"
	}
	if errors.Is(err, context.Canceled) {
		return "error", "context_canceled", "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "error", "context_deadline_exceeded", "none"
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "error", "postgres", dbPostgresErrorSeverity(pgErr)
	}
	return "error", "other", "unknown"
}

func dbPostgresErrorSeverity(pgErr *pgconn.PgError) string {
	if pgErr == nil {
		return "unknown"
	}
	severity := pgErr.SeverityUnlocalized
	if severity == "" {
		severity = pgErr.Severity
	}
	switch strings.ToUpper(severity) {
	case "ERROR":
		return "error"
	case "FATAL":
		return "fatal"
	case "PANIC":
		return "panic"
	case "":
		return "unknown"
	default:
		return "other"
	}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
