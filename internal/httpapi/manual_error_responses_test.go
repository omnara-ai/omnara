package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func TestDaemonRuntimeUnregisteredResponseIncludesStableCode(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := daemonRuntimeUnregisteredResponse().
		VisitSocketMachineDaemonRuntimeResponse(rec); err != nil {
		t.Fatalf("write daemon runtime response: %v", err)
	}
	assertManualErrorResponse(
		t,
		rec,
		http.StatusGone,
		openapi.ErrorCodeDaemonRuntimeUnregistered,
		"daemon runtime is no longer registered for this machine",
	)
}

func TestStreamEventsUnsupportedResponseIncludesStableCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := struct{ http.ResponseWriter }{ResponseWriter: rec}
	if _, ok := any(writer).(http.Flusher); ok {
		t.Fatal("test response writer unexpectedly implements http.Flusher")
	}
	if err := (streamEventsLiveResponse{}).VisitStreamEventsResponse(writer); err != nil {
		t.Fatalf("write unsupported stream response: %v", err)
	}
	assertManualErrorResponse(
		t,
		rec,
		http.StatusServiceUnavailable,
		openapi.ErrorCodeServiceUnavailable,
		"service unavailable: streaming unsupported",
	)
}

func assertManualErrorResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantCode openapi.ErrorCode,
	wantMessage string,
) {
	t.Helper()
	var response openapi.Error
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if rec.Code != wantStatus || response.Code != wantCode || response.Error != wantMessage {
		t.Fatalf(
			"error response status=%d body=%+v, want status=%d code=%q error=%q",
			rec.Code,
			response,
			wantStatus,
			wantCode,
			wantMessage,
		)
	}
}
