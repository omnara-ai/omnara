package log

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/storage"
)

func TestEventDoneLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event", Fields{"test.field": "value"})

	event.Done(context.Background())
	event.Done(context.Background())

	records := logRecords(t, &buf)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0]["message"] != "test.event" {
		t.Fatalf("message = %v", records[0]["message"])
	}
	if records[0]["event.name"] != "test.event" || records[0]["test.field"] != "value" {
		t.Fatalf("record missing expected fields: %+v", records[0])
	}
}

func TestLevelOnlyUsesFirstCall(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event", nil)

	event.Level(WarnLevel)
	event.Level(ErrorLevel)
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	if record["level"] != "warn" {
		t.Fatalf("level = %v, want warn", record["level"])
	}
}

func TestErrorDerivesLevelUnlessExplicitLevelSet(t *testing.T) {
	t.Run("error level", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := WithLogger(context.Background(), testLogger(&buf))
		event := NewEvent(ctx, "test.event", nil)
		event.Error(errors.New("boom"))
		event.Done(context.Background())

		record := oneRecord(t, &buf)
		if record["level"] != "error" {
			t.Fatalf("level = %v, want error", record["level"])
		}
		if record["error.message"] != "boom" {
			t.Fatalf("error.message = %v", record["error.message"])
		}
	})

	t.Run("explicit warn wins", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := WithLogger(context.Background(), testLogger(&buf))
		event := NewEvent(ctx, "test.event", nil)
		event.Level(WarnLevel)
		event.Error(errors.New("boom"))
		event.Done(context.Background())

		record := oneRecord(t, &buf)
		if record["level"] != "warn" {
			t.Fatalf("level = %v, want warn", record["level"])
		}
		if record["error.message"] != "boom" {
			t.Fatalf("error.message = %v", record["error.message"])
		}
	})
}

func TestAttachAfterDoneIgnored(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event", nil)
	event.Done(context.Background())
	event.Attach(Fields{"late": "ignored"})

	record := oneRecord(t, &buf)
	if _, ok := record["late"]; ok {
		t.Fatalf("late attr was logged: %+v", record)
	}
}

func TestContextHelpers(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	if LoggerFromContext(ctx) == Default {
		t.Fatal("LoggerFromContext returned default logger after WithLogger")
	}
	if LoggerFromContext(context.Background()) == nil {
		t.Fatal("LoggerFromContext returned nil")
	}

	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("FromContext returned an event for an empty context")
	}

	event := NewEvent(ctx, "test.event")
	ctx = WithEvent(ctx, event)
	Attach(ctx, Fields{"attached": "yes"})
	Level(ctx, WarnLevel)
	Error(ctx, errors.New("ignored"))
	event.Done(context.Background())

	Attach(context.Background(), Fields{"never": "logged"})
	Error(context.Background(), errors.New("ignored"))
	Level(context.Background(), ErrorLevel)

	record := oneRecord(t, &buf)
	if record["attached"] != "yes" {
		t.Fatalf("attached = %v", record["attached"])
	}
	if record["level"] != "warn" {
		t.Fatalf("level = %v, want warn", record["level"])
	}
	if record["error.message"] != "ignored" {
		t.Fatalf("error.message = %v", record["error.message"])
	}
}

func TestParentEventFields(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	parent := NewEvent(ctx, "parent.event")
	ctx = WithEvent(ctx, parent)
	child := NewEvent(ctx, "child.event")

	child.Done(context.Background())
	parent.Done(context.Background())

	records := logRecords(t, &buf)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	childRecord := records[0]
	if childRecord["parent.event.name"] != "parent.event" {
		t.Fatalf("parent.event.name = %v", childRecord["parent.event.name"])
	}
	if childRecord["parent.event.id"] == "" {
		t.Fatalf("parent.event.id missing: %+v", childRecord)
	}
}

func TestFieldsUseZerologSerialization(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event", Fields{
		"string":   "value",
		"bool":     true,
		"int":      1,
		"duration": time.Second,
		"instant":  time.Unix(1, 0).UTC(),
		"id":       testID(9),
		"nil":      storage.NilID,
	})
	event.Done(context.Background())

	record := oneRecord(t, &buf)
	for _, key := range []string{"string", "bool", "int", "duration", "instant", "id", "nil"} {
		if _, ok := record[key]; !ok {
			t.Fatalf("key %q missing from record %+v", key, record)
		}
	}
	if record["id"] != testID(9).String() {
		t.Fatalf("id = %v, want %s", record["id"], testID(9).String())
	}
	if record["nil"] != nil {
		t.Fatalf("nil = %v, want null", record["nil"])
	}
}

func TestEventDedupsAttrsWithLatestValue(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event", Fields{"duplicate": "initial", "event.duration": "not-final"})
	event.Attach(Fields{"duplicate": "attached"})
	event.beforeDoneFunc(func(f Finalizer) {
		f.Attach(Fields{"duplicate": "finalizer", "event.duration": "still-not-final"})
	})

	event.Done(context.Background())

	raw := bytes.TrimSpace(buf.Bytes())
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		t.Fatalf("decode log object start: %v\n%s", err, string(raw))
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		t.Fatalf("log record is not an object: %s", string(raw))
	}
	counts := make(map[string]int)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			t.Fatalf("decode log key: %v\n%s", err, string(raw))
		}
		key, ok := token.(string)
		if !ok {
			t.Fatalf("log key token = %T %v, want string", token, token)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("decode log value for %s: %v\n%s", key, err, string(raw))
		}
		counts[key]++
	}
	for _, key := range []string{"duplicate", "event.duration"} {
		if counts[key] != 1 {
			t.Fatalf("%s count = %d, want 1 in %s", key, counts[key], string(raw))
		}
	}

	record := oneRecord(t, &buf)
	if record["duplicate"] != "finalizer" {
		t.Fatalf("duplicate = %v, want finalizer in %+v", record["duplicate"], record)
	}
	if record["event.duration"] == "still-not-final" {
		t.Fatalf("derived event.duration did not win: %+v", record)
	}
}

func TestResponseRecorder(t *testing.T) {
	t.Run("defaults to ok", func(t *testing.T) {
		rec := NewResponseRecorder(httptest.NewRecorder())
		if rec.StatusCode() != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.StatusCode())
		}
		if rec.BytesWritten() != 0 {
			t.Fatalf("bytes = %d, want 0", rec.BytesWritten())
		}
	})

	t.Run("tracks status and bytes", func(t *testing.T) {
		raw := httptest.NewRecorder()
		rec := NewResponseRecorder(raw)
		rec.WriteHeader(http.StatusCreated)
		n, err := rec.Write([]byte("hello"))
		if err != nil {
			t.Fatal(err)
		}
		if n != 5 || rec.BytesWritten() != 5 {
			t.Fatalf("n=%d bytes=%d, want 5", n, rec.BytesWritten())
		}
		if rec.StatusCode() != http.StatusCreated || raw.Code != http.StatusCreated {
			t.Fatalf("status recorder=%d raw=%d, want 201", rec.StatusCode(), raw.Code)
		}
	})

	t.Run("write captures implicit ok before late header", func(t *testing.T) {
		raw := httptest.NewRecorder()
		rec := NewResponseRecorder(raw)
		if _, err := rec.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		rec.WriteHeader(http.StatusInternalServerError)
		if rec.StatusCode() != http.StatusOK || raw.Code != http.StatusOK {
			t.Fatalf("status recorder=%d raw=%d, want 200", rec.StatusCode(), raw.Code)
		}
	})

	t.Run("preserves flush for sse style handlers", func(t *testing.T) {
		raw := httptest.NewRecorder()
		rec := NewResponseRecorder(raw)
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("wrapped response writer does not implement http.Flusher")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := w.Write([]byte(": ok\n\n")); err != nil {
				t.Fatal(err)
			}
			flusher.Flush()
		})

		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))

		if rec.StatusCode() != http.StatusOK || rec.BytesWritten() != int64(len(": ok\n\n")) {
			t.Fatalf("status=%d bytes=%d", rec.StatusCode(), rec.BytesWritten())
		}
		if !raw.Flushed {
			t.Fatal("underlying response recorder was not flushed")
		}
		if rec.Unwrap() != raw {
			t.Fatal("Unwrap did not return underlying response writer")
		}
	})
}

func TestHTTPRequestTracksResponseOnDone(t *testing.T) {
	tests := []struct {
		name   string
		status int
		level  string
	}{
		{name: "success", status: http.StatusCreated, level: "info"},
		{name: "client error", status: http.StatusForbidden, level: "warn"},
		{name: "server error", status: http.StatusBadGateway, level: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := WithLogger(context.Background(), testLogger(&buf))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agents?token=secret", nil)
			req.Header.Set("User-Agent", "request-test")
			ctx, rec, event := HTTPRequest(ctx, httptest.NewRecorder(), req)

			if _, ok := FromContext(ctx); !ok {
				t.Fatal("request event missing from returned context")
			}
			rec.WriteHeader(tt.status)
			if _, err := rec.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			event.Done(context.Background())

			record := oneRecord(t, &buf)
			if record["message"] != "http.request" || record["event.name"] != "http.request" {
				t.Fatalf("missing event identity fields: %+v", record)
			}
			if record["http.method"] != http.MethodPost || record["http.path"] != "/api/v1/agents" {
				t.Fatalf("missing request fields: %+v", record)
			}
			if _, ok := record["http.query"]; ok {
				t.Fatalf("query string should not be logged: %+v", record)
			}
			if record["http.user_agent"] != "request-test" {
				t.Fatalf("http.user_agent = %v", record["http.user_agent"])
			}
			if record["level"] != tt.level {
				t.Fatalf("level = %v, want %s: %+v", record["level"], tt.level, record)
			}
			if record["http.status_code"] != float64(tt.status) {
				t.Fatalf("http.status_code = %v, want %d", record["http.status_code"], tt.status)
			}
			if record["http.response_bytes"] != float64(5) {
				t.Fatalf("http.response_bytes = %v, want 5", record["http.response_bytes"])
			}
		})
	}
}

func TestHTTPRequestDefaultsResponseToOK(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	_, _, event := HTTPRequest(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	event.Done(context.Background())

	record := oneRecord(t, &buf)
	if record["level"] != "info" {
		t.Fatalf("level = %v, want info: %+v", record["level"], record)
	}
	if record["http.status_code"] != float64(http.StatusOK) {
		t.Fatalf("http.status_code = %v, want 200", record["http.status_code"])
	}
	if record["http.response_bytes"] != float64(0) {
		t.Fatalf("http.response_bytes = %v, want 0", record["http.response_bytes"])
	}
}

// TestBeforeDoneCallbackCannotDeadlock locks in the no-deadlock invariant:
// even if a beforeDone callback closes over the event and calls public
// (locking) methods on it, Done() must still complete promptly. The locking
// calls noop because done=true by the time the callback runs; only the
// Finalizer mutations land in the emitted record.
func TestBeforeDoneCallbackCannotDeadlock(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")

	event.beforeDoneFunc(func(f Finalizer) {
		f.Attach(Fields{"via.finalizer": "applied"})
		// Pathological: call the locking public API from inside the callback.
		// These must not deadlock; they should noop because done=true.
		event.Attach(Fields{"via.public": "ignored"})
		event.Level(ErrorLevel)
		event.Error(errors.New("late"))
	})

	done := make(chan struct{})
	go func() {
		event.Done(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Done() deadlocked when callback called locking methods")
	}

	record := oneRecord(t, &buf)
	if record["via.finalizer"] != "applied" {
		t.Fatalf("finalizer Attach not applied: %+v", record)
	}
	if _, ok := record["via.public"]; ok {
		t.Fatalf("late public Attach leaked into record: %+v", record)
	}
	if record["level"] != "info" {
		t.Fatalf("level = %v, want info (late Level call must noop): %+v", record["level"], record)
	}
	if _, ok := record["error.message"]; ok {
		t.Fatalf("late Error call leaked into record: %+v", record)
	}
}

func TestConcurrentEventUseLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), testLogger(&buf))
	event := NewEvent(ctx, "test.concurrent", nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event.Attach(Fields{"i": i})
			if i%10 == 0 {
				event.Level(WarnLevel)
			}
			if i == 25 {
				event.Error(errors.New("concurrent error"))
			}
		}(i)
	}
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event.Done(context.Background())
		}()
	}
	wg.Wait()
	event.Done(context.Background())

	records := logRecords(t, &buf)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, jsonHandlerOptions(true)))
}

func oneRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	records := logRecords(t, buf)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(records), buf.String())
	}
	return records[0]
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("unmarshal log record %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func testID(seed byte) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte{seed})
}
