package channelconnector

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationsClientAcceptsOnlyCorrelatedHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		name, body, contentType string
		status                  int
		known                   bool
	}{
		{"capacity", `{"request_id":"request-1","outcome":"failed"}`, "application/json", 503, true},
		{"invalid upload", `{"request_id":"request-1","outcome":"failed"}`, "application/json", 413, true},
		{"proxy error", `{"error":"unavailable"}`, "application/json", 503, false},
		{"wrong request", `{"request_id":"other","outcome":"failed"}`, "application/json", 503, false},
		{"unknown", `{"request_id":"request-1","outcome":"unknown"}`, "application/json", 503, false},
		{"contradictory success", `{"request_id":"request-1","outcome":"completed","payload":{}}`,
			"application/json", 503, false},
		{"payload", `{"request_id":"request-1","outcome":"failed","payload":{}}`, "application/json", 503, false},
		{"duplicate outcome", `{"request_id":"request-1","outcome":"unknown","outcome":"failed"}`,
			"application/json", 503, false},
		{"case alias", `{"request_id":"request-1","OUTCOME":"failed"}`, "application/json", 503, false},
		{"wrong content type", `{"request_id":"request-1","outcome":"failed"}`, "text/plain", 503, false},
		{"redirect", `{"request_id":"request-1","outcome":"failed"}`, "application/json", 307, false},
		{"oversized", strings.Repeat(" ", int(MaxOperationResponseBytes)+1), "application/json", 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				decoded, err := base64.RawURLEncoding.DecodeString(r.Header.Get(OperationRequestIDHeader))
				if err != nil || string(decoded) != "request-1" {
					t.Error("missing request correlation header")
				}
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			result, err := operationClient(t, operationConfig(t, server.URL)).Execute(operationContext(t), operationRequest())
			outcome, code := OperationUnknown, "http_rejected"
			if test.known {
				outcome, code = OperationFailed, "gateway_failed"
			}
			assertOperationError(t, result, err, outcome, code)
			if calls.Load() != 1 {
				t.Fatal("HTTP failure retried the operation")
			}
		})
	}
}

func TestOperationsEarlyRejectionClosesBlockedUpload(t *testing.T) {
	source := &blockedArtifactReader{started: make(chan struct{}), closed: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			t.Error(err)
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(r.Header.Get(OperationRequestIDHeader))
		if err != nil || string(decoded) != "request-雪" {
			t.Error("Unicode request ID changed in transport")
		}
		// Wait until the upload is blocked in an artifact reader. A known early
		// refusal must cancel that reader instead of waiting for the call deadline.
		select {
		case <-source.started:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"request_id":"request-雪","outcome":"failed"}`)
	}))
	defer server.Close()
	request := operationRequest()
	request.RequestID = "request-雪"
	request.Artifacts = []OperationArtifact{{ID: "artifact", Filename: "a.txt", ContentType: "text/plain",
		Open: func(context.Context) (io.ReadCloser, error) { return source, nil },
	}}
	client := operationClient(t, operationConfig(t, server.URL))
	ctx := operationContext(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := client.Execute(ctx, request)
		assertOperationError(t, result, err, OperationFailed, "gateway_failed")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("early rejection waited for the blocked upload")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("early rejection left the artifact source running")
	}
}
