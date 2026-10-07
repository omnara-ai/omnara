package wideevent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CancellationError names why a context was canceled. Pass one to a
// context.CancelCauseFunc so failed database queries report it as
// cancel_source instead of "unknown".
type CancellationError string

const (
	ErrSSEShutdown   CancellationError = "sse_shutdown"
	ErrSocketTimeout CancellationError = "socket_timeout"
	ErrSocketFailure CancellationError = "socket_failure"
	ErrSocketClosed  CancellationError = "socket_closed"
)

type DBQueryTraceRecord struct {
	Name          string
	Start         time.Time
	Duration      time.Duration
	Rows          int64
	ErrorKind     string
	ErrorSeverity string

	SQLState        string
	CancelSource    string
	RequestCanceled bool
	Failed          bool
}

func AttachDBQuery(ctx context.Context, record DBQueryTraceRecord) {
	event, ok := FromContext(ctx)
	if !ok {
		return
	}
	event.attachDBQuery(record)
}

type HTTPRequestTraceRecord struct {
	Method     string
	Host       string
	Path       string
	StatusCode int
	Start      time.Time
	Duration   time.Duration
	Error      string
}

func AttachHTTPRequest(ctx context.Context, record HTTPRequestTraceRecord) {
	event, ok := FromContext(ctx)
	if !ok {
		return
	}
	event.attachHTTPRequest(record)
}

const maxTraceRecords = 25

// traceStats totals every attached record, so the kept record lists can stop
// growing at maxTraceRecords instead of holding every record until Done.
type traceStats struct {
	count      int
	errorCount int
	msSum      int64
	msMax      int64
}

func (s *traceStats) add(duration time.Duration, failed bool) {
	s.count++
	if failed {
		s.errorCount++
	}
	ms := duration.Milliseconds()
	s.msSum += ms
	s.msMax = max(s.msMax, ms)
}

// keepDBQuery bounds the kept queries at maxTraceRecords. Once full, a failure
// replaces the most recent kept success, so failures win while the earliest
// successes and chronological order survive.
func keepDBQuery(queries []DBQueryTraceRecord, record DBQueryTraceRecord) []DBQueryTraceRecord {
	if len(queries) < maxTraceRecords {
		return append(queries, record)
	}
	if !record.Failed {
		return queries
	}
	for i := len(queries) - 1; i >= 0; i-- {
		if !queries[i].Failed {
			return append(append(queries[:i], queries[i+1:]...), record)
		}
	}
	return queries
}

func (e *Event) attachDBQuery(record DBQueryTraceRecord) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	if e.requestCanceled != nil {
		record.RequestCanceled = e.requestCanceled()
	}
	e.dbStats.add(record.Duration, record.Failed)
	e.dbRows += record.Rows
	e.dbQueries = keepDBQuery(e.dbQueries, record)
}

func (e *Event) attachHTTPRequest(record HTTPRequestTraceRecord) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.httpStats.add(record.Duration, record.Error != "")
	if len(e.httpReqs) < maxTraceRecords {
		e.httpReqs = append(e.httpReqs, record)
	}
}

func (e *Event) flushTraceFields() {
	e.flushDBQueries()
	e.flushHTTPRequests()
}

func (e *Event) flushDBQueries() {
	if e.dbStats.count == 0 {
		return
	}

	out := Fields{
		"db.queries.count":           e.dbStats.count,
		"db.queries.truncated_count": e.dbStats.count - len(e.dbQueries),
	}
	for i, record := range e.dbQueries {
		prefix := fmt.Sprintf("db.queries.%d.", i)
		out[prefix+"name"] = record.Name
		out[prefix+"start_time_ms"] = relativeMilliseconds(record.Start, e.started)
		out[prefix+"duration_ms"] = record.Duration.Milliseconds()
		out[prefix+"rows"] = record.Rows
		if record.Failed {
			out[prefix+"error_kind"] = record.ErrorKind
			if record.SQLState != "" {
				out[prefix+"sqlstate"] = record.SQLState
			}
			if e.requestCanceled != nil {
				out[prefix+"request_canceled"] = record.RequestCanceled
			}
			if record.CancelSource != "" {
				out[prefix+"cancel_source"] = record.CancelSource
			}
			if record.ErrorSeverity != "none" {
				out[prefix+"error_severity"] = record.ErrorSeverity
			}
		}
	}
	out["db.queries.error_count"] = e.dbStats.errorCount
	out["db.queries.duration_ms_sum"] = e.dbStats.msSum
	out["db.queries.duration_ms_max"] = e.dbStats.msMax
	out["db.queries.rows_sum"] = e.dbRows

	e.applyFields(out)
}

func (e *Event) flushHTTPRequests() {
	if e.httpStats.count == 0 {
		return
	}

	out := Fields{
		"http.subrequests.count":           e.httpStats.count,
		"http.subrequests.truncated_count": e.httpStats.count - len(e.httpReqs),
	}
	for i, record := range e.httpReqs {
		prefix := fmt.Sprintf("http.subrequests.%d.", i)
		out[prefix+"method"] = record.Method
		out[prefix+"host"] = record.Host
		out[prefix+"path"] = record.Path
		out[prefix+"start_time_ms"] = relativeMilliseconds(record.Start, e.started)
		out[prefix+"total_ms"] = record.Duration.Milliseconds()
		if record.StatusCode != 0 {
			out[prefix+"status_code"] = record.StatusCode
		}
		if record.Error != "" {
			out[prefix+"error"] = record.Error
		}
	}
	out["http.subrequests.error_count"] = e.httpStats.errorCount
	out["http.subrequests.total_ms_sum"] = e.httpStats.msSum
	out["http.subrequests.total_ms_max"] = e.httpStats.msMax

	e.applyFields(out)
}

func relativeMilliseconds(t, base time.Time) int64 {
	if t.IsZero() || base.IsZero() {
		return 0
	}
	return t.Sub(base).Milliseconds()
}

func (s CancellationError) Error() string { return string(s) }

func DBCancellationSource(ctx context.Context) string {
	cause := context.Cause(ctx)
	var source CancellationError
	if errors.As(cause, &source) {
		return string(source)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return "context_deadline"
	}
	return "unknown"
}

func DBQueryName(sql string) string {
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
