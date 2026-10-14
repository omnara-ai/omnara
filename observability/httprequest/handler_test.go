package httprequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/omnara-ai/omnara/observability/wideevent"
)

func TestHandlerEmitsRoutedRequestEvent(t *testing.T) {
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orgs/{org}/test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short"))
	})
	handler := Handler(Config{Logger: testLogger(&logs)}, mux)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orgs/org_123/test?token=secret", nil))

	event := oneLogRecord(t, &logs)
	for key, want := range map[string]any{
		"message":             "http.request",
		"level":               "warn",
		"http.path":           "/orgs/org_123/test",
		"http.route":          "GET /orgs/{org}/test",
		"http.status_code":    float64(http.StatusTeapot),
		"http.response_bytes": float64(len("short")),
	} {
		if event[key] != want {
			t.Fatalf("%s = %v, want %v in %+v", key, event[key], want, event)
		}
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatalf("query string was logged: %s", logs.String())
	}
}

func TestHandlerWritesInternalErrorForPanic(t *testing.T) {
	var logs bytes.Buffer
	handler := Handler(Config{
		Logger: testLogger(&logs),
		WriteInternalError: func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal"}`))
		},
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if response.Code != http.StatusInternalServerError || response.Body.String() != `{"error":"internal"}` {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	event := oneLogRecord(t, &logs)
	if event["level"] != "error" || event["error.message"] != "http handler panicked: boom" ||
		event["http.status_code"] != float64(http.StatusInternalServerError) {
		t.Fatalf("event = %+v", event)
	}
	if stack, _ := event["error.stack"].(string); stack == "" {
		t.Fatalf("panic stack missing: %+v", event)
	}
}

func TestHandlerDefaultsInternalErrorResponse(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	handler := Handler(Config{Logger: testLogger(&bytes.Buffer{})}, panicking)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestHandlerAbortsStartedResponseOnPanic(t *testing.T) {
	var logs bytes.Buffer
	handler := Handler(Config{Logger: testLogger(&logs)}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wideevent.Level(r.Context(), wideevent.DebugLevel)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"partial":`))
		panic("boom")
	}))

	recovered := serveRecovering(handler, httptest.NewRequest(http.MethodPost, "/panic", nil))

	if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("recovered = %v, want %v", recovered, http.ErrAbortHandler)
	}
	event := oneLogRecord(t, &logs)
	if event["level"] != "error" || event["http.response_aborted"] != true ||
		event["http.written_status_code"] != float64(http.StatusCreated) ||
		event["http.status_code"] != float64(http.StatusInternalServerError) {
		t.Fatalf("event = %+v", event)
	}
}

func TestHandlerPropagatesAbortHandler(t *testing.T) {
	var logs bytes.Buffer
	handler := Handler(Config{Logger: testLogger(&logs)}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	recovered := serveRecovering(handler, httptest.NewRequest(http.MethodGet, "/abort", nil))

	if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("recovered = %v, want %v", recovered, http.ErrAbortHandler)
	}
	if strings.Contains(logs.String(), "http handler panicked") {
		t.Fatalf("abort sentinel was logged as a panic: %s", logs.String())
	}
}

func TestHandlerRecordsMetricsFromTheEventStatus(t *testing.T) {
	set := metrics.New(metrics.WithNamespace("test"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orgs/{org}/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /orgs/{org}/guarded", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /canceled", func(_ http.ResponseWriter, request *http.Request) {
		wideevent.Error(request.Context(), context.Canceled)
	})
	mux.HandleFunc("POST /aborted", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		panic("boom")
	})
	guard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/guarded") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r.WithContext(r.Context()))
	})
	handler := Handler(Config{
		Logger:  testLogger(&bytes.Buffer{}),
		Metrics: metrics.NewHTTPRecorder(set, metrics.SubsystemAPI),
		Mux:     mux,
	}, guard)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orgs/org_123/ok", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orgs/org_123/guarded", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/not-routed", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/canceled", nil))
	serveRecovering(handler, httptest.NewRequest(http.MethodPost, "/aborted", nil))

	scrape := httptest.NewRecorder()
	set.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
	body := scrape.Body.String()
	for _, want := range []string{
		`test_api_requests_total{code="200",method="get",route="/orgs/{org}/ok"} 1`,
		`test_api_requests_total{code="401",method="get",route="/orgs/{org}/guarded"} 1`,
		`test_api_requests_total{code="404",method="get",route="unmatched"} 1`,
		`test_api_requests_total{code="499",method="get",route="/canceled"} 1`,
		`test_api_requests_total{code="500",method="post",route="/aborted"} 1`,
		`test_api_request_duration_seconds_count{code="500",method="post",route="/aborted"} 1`,
		`test_api_requests_in_flight 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "org_123") || strings.Contains(body, "not-routed") {
		t.Fatalf("raw request path leaked into metrics:\n%s", body)
	}
}

func serveRecovering(handler http.Handler, request *http.Request) (recovered any) {
	defer func() { recovered = recover() }()
	handler.ServeHTTP(httptest.NewRecorder(), request)
	return nil
}

func testLogger(output *bytes.Buffer) *slog.Logger {
	return slog.New(wideevent.NewJSONHandler(output, nil))
}

func oneLogRecord(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log records = %d, want 1: %s", len(lines), logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	return record
}
