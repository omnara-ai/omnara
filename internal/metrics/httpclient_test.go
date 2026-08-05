package metrics

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
	"time"

	"github.com/omnara-ai/omnara/internal/log"
)

func TestHTTPClientResultClassification(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		err      error
		wantCode string
		wantRes  string
		wantKind string
	}{
		{"success 200", http.StatusOK, nil, "200", "success", "none"},
		{"redirect 301", http.StatusMovedPermanently, nil, "301", "success", "none"},
		{"client error 404", http.StatusNotFound, nil, "404", "error", "http_4xx"},
		{"server error 503", http.StatusServiceUnavailable, nil, "503", "error", "http_5xx"},
		{"context canceled", 0, context.Canceled, "none", "error", "context_canceled"},
		{"deadline exceeded", 0, context.DeadlineExceeded, "none", "error", "context_deadline_exceeded"},
		{"transport error", 0, errors.New("dial: connection refused"), "none", "error", "transport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errorKind string
			if tt.err != nil {
				errorKind = httpRequestErrorKind(tt.err)
			}
			code, result, kind := httpRequestResult(tt.status, errorKind)
			if code != tt.wantCode || result != tt.wantRes || kind != tt.wantKind {
				t.Fatalf("httpRequestResult()=(%q,%q,%q) want=(%q,%q,%q)", code, result, kind, tt.wantCode, tt.wantRes, tt.wantKind)
			}
		})
	}
}

func TestHTTPClientRecorderEmitsMetricsWithWildcardPath(t *testing.T) {
	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	now := time.Unix(100, 0)
	recorder.now = func() time.Time { return now }

	var advance time.Duration
	client := NewObservedHTTPClient(
		&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			now = now.Add(advance)
			switch {
			case req.URL.Path == "/ok":
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			case strings.HasPrefix(req.URL.Path, "/notfound"):
				return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody}, nil
			default:
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody}, nil
			}
		})},
		recorder,
	)

	doRequest := func(method, path string, requestDuration time.Duration) {
		req, _ := http.NewRequestWithContext(context.Background(), method, "https://api.example.com"+path, nil)
		advance = requestDuration
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("do %s %s: %v", method, path, err)
		}
		resp.Body.Close()
	}

	doRequest(http.MethodGet, "/ok?id=123", 25*time.Millisecond)
	doRequest(http.MethodPost, "/notfound/user_456", 10*time.Millisecond)
	doRequest(http.MethodGet, "/boom", 30*time.Millisecond)

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_http_client_requests_total{code="200",error_kind="none",host="api.example.com",` +
			`method="GET",path="/*",result="success"} 1`,
		`omnara_http_client_requests_total{code="404",error_kind="http_4xx",host="api.example.com",` +
			`method="POST",path="/*",result="error"} 1`,
		`omnara_http_client_requests_total{code="500",error_kind="http_5xx",host="api.example.com",` +
			`method="GET",path="/*",result="error"} 1`,
		`omnara_http_client_request_duration_seconds_count{code="200",error_kind="none",` +
			`host="api.example.com",method="GET",path="/*",result="success"} 1`,
		`omnara_http_client_request_duration_seconds_bucket{code="200",error_kind="none",` +
			`host="api.example.com",method="GET",path="/*",result="success",le="0.025"} 1`,
		`omnara_http_client_request_duration_seconds_bucket{code="500",error_kind="http_5xx",` +
			`host="api.example.com",method="GET",path="/*",result="error",le="0.05"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "user_456") || strings.Contains(body, "id=123") {
		t.Fatalf("raw path or query leaked into metrics:\n%s", body)
	}
}

func TestHTTPClientRecorderRecordsTransportError(t *testing.T) {
	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	recorder.now = time.Now

	client := NewObservedHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("dial: connection refused")
		}),
	}, recorder)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.com/v1/x", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error")
	}

	body := scrapeMetrics(t, set)
	want := `omnara_http_client_requests_total{code="none",error_kind="transport",` +
		`host="api.example.com",method="GET",path="/*",result="error"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("metrics missing %q:\n%s", want, body)
	}
}

func TestHTTPClientRecorderScrubsTraceErrors(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	ctx := log.WithLogger(context.Background(), logger)
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	client := NewObservedHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New(
				"dial https://api.example.com/v1/messages?token=tok_123 failed: password=hunter2 code=${code}",
			)
		}),
	}, recorder)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/v1/messages?token=request", nil)
	_, _ = client.Do(req)
	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	got, _ := record["http.subrequests.0.error"].(string)
	for _, forbidden := range []string{"tok_123", "hunter2", "${code}", "token=tok_123"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("http trace error leaked %q in %q", forbidden, got)
		}
	}
}

func TestHTTPClientRecorderUsesEndpointLabelOption(t *testing.T) {
	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	client := NewObservedHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	}, recorder, WithHTTPClientPathLabel("/v1/messages/{messageID}"))
	req, _ := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://api.example.com/v1/messages/msg_123?include=body",
		nil,
	)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()

	body := scrapeMetrics(t, set)
	want := `omnara_http_client_requests_total{code="200",error_kind="none",host="api.example.com",` +
		`method="GET",path="/v1/messages/{messageID}",result="success"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("metrics missing %q:\n%s", want, body)
	}
	if strings.Contains(body, "msg_123") || strings.Contains(body, "include=body") {
		t.Fatalf("raw endpoint leaked into metrics:\n%s", body)
	}
}

func TestHTTPClientRecorderAttachesWideEventRequestData(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	ctx := log.WithLogger(context.Background(), logger)
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)

	now := time.Now()
	recorder.now = func() time.Time { return now }

	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		now = now.Add(38 * time.Millisecond)
		return &http.Response{StatusCode: 529, Body: http.NoBody}, nil
	})
	client := NewObservedHTTPClient(&http.Client{Transport: base}, recorder)

	now = now.Add(47 * time.Millisecond)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	now = now.Add(1050 * time.Millisecond)
	client = NewObservedHTTPClient(
		&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			now = now.Add(4180 * time.Millisecond)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})},
		recorder,
		WithHTTPClientPathLabel("/v1/messages"),
	)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	assertJSONNumber(t, record, "http.subrequests.count", 2)
	assertJSONNumber(t, record, "http.subrequests.0.total_ms", 38)
	assertJSONNumber(t, record, "http.subrequests.0.status_code", 529)
	if got := record["http.subrequests.0.host"]; got != "api.anthropic.com" {
		t.Fatalf("http.subrequests.0.host = %v, want api.anthropic.com", got)
	}
	if got := record["http.subrequests.0.path"]; got != "/*" {
		t.Fatalf("http.subrequests.0.path = %v, want /*", got)
	}
	assertJSONNumber(t, record, "http.subrequests.1.total_ms", 4180)
	if got := record["http.subrequests.1.path"]; got != "/v1/messages" {
		t.Fatalf("http.subrequests.1.path = %v, want /v1/messages", got)
	}
	assertJSONNumber(t, record, "http.subrequests.total_ms_sum", 4218)
}

func scrapeMetrics(t *testing.T, set *Set) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, ScrapePath, nil)
	resp := httptest.NewRecorder()
	set.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("scrape status=%d want=%d", resp.Code, http.StatusOK)
	}
	return resp.Body.String()
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
