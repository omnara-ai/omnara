package log

import (
	"context"
	"fmt"
	"time"
)

type DBQueryTraceRecord struct {
	Name          string
	Start         time.Time
	Duration      time.Duration
	Rows          int64
	Error         string
	ErrorKind     string
	ErrorSeverity string
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

func (e *Event) attachDBQuery(record DBQueryTraceRecord) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.dbQueries = append(e.dbQueries, record)
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
	e.httpReqs = append(e.httpReqs, record)
}

func (e *Event) flushTraceFields() {
	e.flushDBQueries()
	e.flushHTTPRequests()
}

func (e *Event) flushDBQueries() {
	if len(e.dbQueries) == 0 {
		return
	}

	emitCount := min(len(e.dbQueries), maxTraceRecords)
	out := Fields{
		"db.queries.count":           len(e.dbQueries),
		"db.queries.truncated_count": len(e.dbQueries) - emitCount,
	}
	var durationMsSum int64
	var durationMsMax int64
	var rowsSum int64
	var errorCount int
	for i, record := range e.dbQueries {
		durationMs := record.Duration.Milliseconds()
		durationMsSum += durationMs
		if durationMs > durationMsMax {
			durationMsMax = durationMs
		}
		rowsSum += record.Rows
		if record.Error != "" {
			errorCount++
		}
		if i >= emitCount {
			continue
		}
		prefix := fmt.Sprintf("db.queries.%d.", i)
		out[prefix+"name"] = record.Name
		out[prefix+"start_time_ms"] = relativeMilliseconds(record.Start, e.started)
		out[prefix+"duration_ms"] = durationMs
		out[prefix+"rows"] = record.Rows
		if record.Error != "" {
			out[prefix+"error"] = record.Error
			out[prefix+"error_kind"] = record.ErrorKind
			if record.ErrorSeverity != "none" {
				out[prefix+"error_severity"] = record.ErrorSeverity
			}
		}
	}
	out["db.queries.error_count"] = errorCount
	out["db.queries.duration_ms_sum"] = durationMsSum
	out["db.queries.duration_ms_max"] = durationMsMax
	out["db.queries.rows_sum"] = rowsSum

	e.applyFields(out)
}

func (e *Event) flushHTTPRequests() {
	if len(e.httpReqs) == 0 {
		return
	}

	emitCount := min(len(e.httpReqs), maxTraceRecords)
	out := Fields{
		"http.subrequests.count":           len(e.httpReqs),
		"http.subrequests.truncated_count": len(e.httpReqs) - emitCount,
	}
	var totalMsSum int64
	var totalMsMax int64
	var errorCount int
	for i, record := range e.httpReqs {
		totalMs := record.Duration.Milliseconds()
		totalMsSum += totalMs
		if totalMs > totalMsMax {
			totalMsMax = totalMs
		}
		if record.Error != "" {
			errorCount++
		}
		if i >= emitCount {
			continue
		}
		prefix := fmt.Sprintf("http.subrequests.%d.", i)
		out[prefix+"method"] = record.Method
		out[prefix+"host"] = record.Host
		out[prefix+"path"] = record.Path
		out[prefix+"start_time_ms"] = relativeMilliseconds(record.Start, e.started)
		out[prefix+"total_ms"] = totalMs
		if record.StatusCode != 0 {
			out[prefix+"status_code"] = record.StatusCode
		}
		if record.Error != "" {
			out[prefix+"error"] = record.Error
		}
	}
	out["http.subrequests.error_count"] = errorCount
	out["http.subrequests.total_ms_sum"] = totalMsSum
	out["http.subrequests.total_ms_max"] = totalMsMax

	e.applyFields(out)
}

func relativeMilliseconds(t, base time.Time) int64 {
	if t.IsZero() || base.IsZero() {
		return 0
	}
	return t.Sub(base).Milliseconds()
}
