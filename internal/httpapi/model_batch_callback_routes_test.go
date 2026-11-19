package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelbatch"
)

func openRouterBatchCallbackRequest(signature string) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/model-batches/openrouter/"+uuid.NewString()+"/token",
		strings.NewReader(`{"id":"evt_1","object":"event","type":"batch.completed","data":{"id":"batch_123"}}`),
	)
	if signature != "" {
		request.Header.Set(modelbatch.SignatureHeader, signature)
	}
	return request
}

func TestOpenRouterBatchCallbackRejectsUnsignedEventWhenSecretConfigured(t *testing.T) {
	server := &Server{}
	WithOpenRouterBatchWebhookSecret("signing-secret")(server)
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+modelbatch.OpenRouterCallbackPathPattern, server.openRouterBatchCallbackRoute)

	for _, signature := range []string{"", "t=1,v1=00"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, openRouterBatchCallbackRequest(signature))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("signature %q: status = %d, want 401", signature, recorder.Code)
		}
	}
}

func TestOpenRouterBatchCallbackAsksForRedeliveryWithoutStore(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+modelbatch.OpenRouterCallbackPathPattern, (&Server{}).openRouterBatchCallbackRoute)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, openRouterBatchCallbackRequest(""))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 so OpenRouter retries the delivery", recorder.Code)
	}
}

func TestOpenRouterBatchCallbackRouteDeclaresProviderSignedAccess(t *testing.T) {
	// The worker builds callback URLs from the shared pattern; the served route
	// must stay the same path.
	mux := http.NewServeMux()
	(&Server{}).registerRoutes(mux)
	_, pattern := mux.Handler(openRouterBatchCallbackRequest(""))
	if pattern != "POST "+modelbatch.OpenRouterCallbackPathPattern {
		t.Fatalf("batch callback URL is served by %q, want the shared callback pattern", pattern)
	}
	for _, contract := range serverManualRouteContracts {
		if contract.Pattern == modelbatch.OpenRouterCallbackPathPattern {
			if contract.Method != http.MethodPost || contract.Access != manualRouteAccessProviderSigned {
				t.Fatalf("batch callback contract = %+v", contract)
			}
			return
		}
	}
	t.Fatal("batch callback route has no manual route contract")
}
