package log

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestTracingFlushesFlattenedRecordsAndAggregates(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")
	ctx = WithEvent(ctx, event)

	AttachDBQuery(ctx, DBQueryTraceRecord{
		Name:     "GetAgent",
		Start:    event.started,
		Duration: 12 * time.Millisecond,
		Rows:     1,
	})
	AttachDBQuery(ctx, DBQueryTraceRecord{
		Name:     "InsertAgent",
		Start:    event.started.Add(12 * time.Millisecond),
		Duration: 30 * time.Millisecond,
		Rows:     0,
		Error:    "duplicate key",
	})
	AttachHTTPRequest(ctx, HTTPRequestTraceRecord{
		Method:   "POST",
		Host:     "api.anthropic.com",
		Start:    event.started,
		Duration: 38 * time.Millisecond,
	})
	AttachHTTPRequest(ctx, HTTPRequestTraceRecord{
		Method:   "POST",
		Host:     "api.anthropic.com",
		Start:    event.started.Add(38 * time.Millisecond),
		Duration: 4180 * time.Millisecond,
	})

	event.Done(context.Background())
	record := oneRecord(t, &buf)

	for key, want := range map[string]any{
		"db.queries.count":                 2,
		"db.queries.truncated_count":       0,
		"db.queries.0.name":                "GetAgent",
		"db.queries.1.error":               "duplicate key",
		"db.queries.duration_ms_sum":       int64(42),
		"db.queries.duration_ms_max":       int64(30),
		"db.queries.rows_sum":              int64(1),
		"db.queries.error_count":           1,
		"http.subrequests.count":           2,
		"http.subrequests.truncated_count": 0,
		"http.subrequests.0.method":        "POST",
		"http.subrequests.1.total_ms":      int64(4180),
		"http.subrequests.total_ms_sum":    int64(4218),
		"http.subrequests.total_ms_max":    int64(4180),
		"http.subrequests.error_count":     0,
	} {
		assertDecodedField(t, record, key, want)
	}
}

func TestTracingCapsFlattenedRecordsAndKeepsAggregates(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")
	ctx = WithEvent(ctx, event)

	for i := range 75 {
		AttachDBQuery(ctx, DBQueryTraceRecord{
			Name:     "Query",
			Start:    event.started.Add(time.Duration(i) * time.Millisecond),
			Duration: time.Duration(i+1) * time.Millisecond,
			Rows:     1,
		})
	}

	event.Done(context.Background())
	record := oneRecord(t, &buf)

	for key, want := range map[string]any{
		"db.queries.count":           75,
		"db.queries.truncated_count": 50,
		"db.queries.duration_ms_sum": int64(2850),
		"db.queries.duration_ms_max": int64(75),
		"db.queries.rows_sum":        int64(75),
	} {
		assertDecodedField(t, record, key, want)
	}
	if _, ok := record["db.queries.24.name"]; !ok {
		t.Fatalf("expected last emitted query field in %+v", record)
	}
	if _, ok := record["db.queries.25.name"]; ok {
		t.Fatalf("query past emission cap was logged: %+v", record)
	}
}

func TestTracingNoopWithoutEvent(t *testing.T) {
	ctx := context.Background()
	AttachDBQuery(ctx, DBQueryTraceRecord{Name: "NoEvent", Duration: time.Millisecond})

	var buf bytes.Buffer
	ctx = WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")
	ctx = WithEvent(ctx, event)

	AttachHTTPRequest(ctx, HTTPRequestTraceRecord{Host: "a", Duration: time.Millisecond})
	AttachHTTPRequest(ctx, HTTPRequestTraceRecord{Host: "b", Duration: 2 * time.Millisecond})

	event.Done(context.Background())
	record := oneRecord(t, &buf)
	assertDecodedField(t, record, "http.subrequests.count", 2)
	assertDecodedField(t, record, "http.subrequests.total_ms_sum", int64(3))
}

func TestTracingNoFieldsWhenNoRecords(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")

	event.Done(context.Background())
	record := oneRecord(t, &buf)
	for _, key := range []string{"db.queries.count", "http.subrequests.count"} {
		if _, ok := record[key]; ok {
			t.Fatalf("expected no %s field when no records attached; got %+v", key, record)
		}
	}
}

func assertDecodedField(t *testing.T, record map[string]any, key string, want any) {
	t.Helper()
	got, ok := record[key]
	if !ok {
		t.Fatalf("missing field %q in record: %+v", key, record)
	}
	switch w := want.(type) {
	case int:
		if g, ok := got.(float64); !ok || int(g) != w {
			t.Fatalf("field %q = %v (%T), want %d", key, got, got, w)
		}
	case int64:
		if g, ok := got.(float64); !ok || int64(g) != w {
			t.Fatalf("field %q = %v (%T), want %d", key, got, got, w)
		}
	default:
		if got != want {
			t.Fatalf("field %q = %v, want %v", key, got, want)
		}
	}
}
