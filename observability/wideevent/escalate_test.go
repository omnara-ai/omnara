package wideevent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEscalateOverridesLowerPinnedLevel(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	ctx, event := Start(ctx, "test.event")
	Level(ctx, DebugLevel)
	Escalate(ctx, ErrorLevel)
	Escalate(ctx, WarnLevel)
	Level(ctx, InfoLevel)

	event.Done(ctx)

	record := oneRecord(t, &buf)
	if record["level"] != "error" {
		t.Fatalf("level = %v, want error", record["level"])
	}
	if record["event.name"] != "test.event" {
		t.Fatalf("event.name = %v", record["event.name"])
	}
}

func TestEscalateNeverLowersErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	ctx, event := Start(ctx, "test.event")
	Escalate(ctx, WarnLevel)
	Error(ctx, errors.New("boom"))

	event.Done(ctx)

	if record := oneRecord(t, &buf); record["level"] != "error" {
		t.Fatalf("level = %v, want error", record["level"])
	}
}

func TestEscalateRaisesDefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	ctx, event := Start(ctx, "test.event")
	Escalate(ctx, WarnLevel)

	event.Done(ctx)

	if record := oneRecord(t, &buf); record["level"] != "warn" {
		t.Fatalf("level = %v, want warn", record["level"])
	}
}

func TestEscalateAfterDoneIgnored(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	event := NewEvent(ctx, "test.event")
	event.Done(ctx)
	event.Escalate(ErrorLevel)
	if record := oneRecord(t, &buf); record["level"] != "info" {
		t.Fatalf("level = %v, want info", record["level"])
	}
}

func TestDoneSkipsDisabledLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := WithLogger(t.Context(), logger)
	ctx, event := Start(ctx, "test.event")
	AttachDBQuery(ctx, DBQueryTraceRecord{Name: "Query"})
	Level(ctx, DebugLevel)

	event.Done(ctx)

	if buf.Len() != 0 {
		t.Fatalf("disabled event logged: %s", buf.String())
	}
}

func TestHTTPRequestServerErrorOverridesPinnedLevel(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	ctx, rec, event := HTTPRequest(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	Level(ctx, DebugLevel)
	rec.WriteHeader(http.StatusServiceUnavailable)

	event.Done(t.Context())

	if record := oneRecord(t, &buf); record["level"] != "error" {
		t.Fatalf("level = %v, want error: %+v", record["level"], record)
	}
}

func TestHTTPRequestAbortReportsServerError(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(t.Context(), testLogger(&buf))
	_, rec, event := HTTPRequest(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	rec.WriteHeader(http.StatusOK)
	rec.Abort()

	event.Done(t.Context())

	record := oneRecord(t, &buf)
	assertDecodedField(t, record, "http.status_code", http.StatusInternalServerError)
	assertDecodedField(t, record, "http.written_status_code", http.StatusOK)
	if record["http.response_aborted"] != true || record["level"] != "error" {
		t.Fatalf("aborted response not reported as error: %+v", record)
	}
	if got := rec.TelemetryStatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("telemetry status = %d, want 500", got)
	}
}

func TestOnlyCanceled(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, true},
		{"wrapped", fmt.Errorf("query: %w", context.Canceled), true},
		{"joined cancellations", errors.Join(context.Canceled, fmt.Errorf("x: %w", context.Canceled)), true},
		{"joined failure", errors.Join(context.Canceled, errors.New("boom")), false},
		{"deadline", context.DeadlineExceeded, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnlyCanceled(tt.err); got != tt.want {
				t.Fatalf("OnlyCanceled() = %v, want %v", got, tt.want)
			}
		})
	}
}
