package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
)

func TestAPIDispatchEmitsRequestEvent(t *testing.T) {
	buf, log := newRequestEventCapture()
	server, err := New(
		log,
		nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
	)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	t.Cleanup(server.Close)
	server.Handler()

	request := httptest.NewRequest(http.MethodGet, openAPIBasePath+"/orgs", nil)
	request = request.WithContext(apimcp.ContextWithToolCall(
		request.Context(),
		apimcp.ToolCall{Tool: "orgs_list", OperationID: "ListOrganizations"},
	))
	recorder := httptest.NewRecorder()
	server.dispatchAPIRequest(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without a principal", recorder.Code)
	}
	event := decodeRequestEvent(t, buf)
	want := map[string]any{
		"event.name":           "http.request",
		"http.method":          http.MethodGet,
		"http.path":            openAPIBasePath + "/orgs",
		"http.status_code":     float64(http.StatusForbidden),
		"mcp.tool":             "orgs_list",
		"openapi.operation_id": "ListOrganizations",
		"level":                "warn",
	}
	for key, value := range want {
		if event[key] != value {
			t.Errorf("%s = %v, want %v", key, event[key], value)
		}
	}
}
