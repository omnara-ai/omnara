package anthropicmessages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

func TestRespondClassifiesAnthropicErrorsByEvidencePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       model.ErrorKind
	}{
		{
			name:       "billing",
			statusCode: http.StatusPaymentRequired,
			body:       `{"type":"error","error":{"type":"billing_error","message":"pay up"},"request_id":"req_bill"}`,
			want:       model.ErrorKindBillingAccount,
		},
		{
			name:       "generic context prose is invalid request",
			statusCode: http.StatusBadRequest,
			body:       `{"type":"error","error":{"type":"invalid_request_error","message":"prompt exceeds context window"}}`,
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "anthropic prompt too long wire error",
			statusCode: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"message":"prompt is too long: 201312 tokens > 200000 maximum"}}`,
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "generic gateway type with explicit context prose",
			statusCode: http.StatusBadGateway,
			body: `{"type":"error","error":{"type":"api_error",` +
				`"message":"Your input exceeds the context window of this model."}}`,
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "generic gateway type with explicit payload prose",
			statusCode: http.StatusBadGateway,
			body: `{"type":"error","error":{"type":"api_error",` +
				`"message":"Request body too large for the upstream provider."}}`,
			want: model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "anthropic input plus output exceeds context wire error",
			statusCode: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"message":"input length and max_tokens exceed context limit: 190000 + 20000 > 200000"}}`,
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "anthropic input plus output quoted field wire error",
			statusCode: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				"\"message\":\"input length and `max_tokens` exceed context limit: 198482 + 8192 > 200000\"}}",
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "typed context",
			statusCode: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"error_type":"context_length_exceeded","message":"prompt exceeds context window"}}`,
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "request too large type",
			statusCode: http.StatusRequestEntityTooLarge,
			body: `{"type":"error","error":{"type":"request_too_large",` +
				`"message":"Request exceeds the maximum size"},"request_id":"req_large"}`,
			want: model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "request entity too large status is decisive",
			statusCode: http.StatusRequestEntityTooLarge,
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"message":"Request exceeds the maximum size"},` +
				`"request_id":"req_large_status"}`,
			want: model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "rate",
			statusCode: http.StatusTooManyRequests,
			body:       `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "timeout",
			statusCode: http.StatusGatewayTimeout,
			body:       `{"type":"error","error":{"type":"api_error","message":"timeout"}}`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "unknown gateway timeout standardized as unavailable",
			statusCode: http.StatusGatewayTimeout,
			body:       `{"type":"error","error":{"message":"gateway timeout"}}`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "overloaded",
			statusCode: 529,
			body:       `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "malformed rate",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "partially decoded malformed rate",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"type":"context_length_exceeded"},`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "malformed upstream",
			statusCode: http.StatusBadGateway,
			body:       `<html>bad gateway`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "redirect",
			statusCode: http.StatusTemporaryRedirect,
			body:       `redirect refused`,
			want:       model.ErrorKindInvalidRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := testRespondClient(server)
			_, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != tt.want || providerErr.Source != server.URL ||
				providerErr.StatusCode != tt.statusCode {
				t.Fatalf("classified error = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestRespondPreservesAnthropicErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Request-Id", "req_header")
		w.Header().Set("Retry-After", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(
			`{"type":"error","request_id":"req_body","retry_after":41,` +
				`"error":{"type":"rate_limit_error","message":"slow down",` +
				`"request_id":"req_nested","retry_after":42}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.RequestID != "req_header" || providerErr.RetryAfter == nil ||
		providerErr.RetryAfter.HTTPDate == nil || providerErr.RetryAfter.DeltaSeconds != nil {
		t.Fatalf("anthropic provider evidence = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondRetainsTopLevelRequestIDButIgnoresBodyRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(
			`{"type":"error","request_id":"req_body","retry_after":41,` +
				`"error":{"type":"rate_limit_error","message":"slow down",` +
				`"request_id":"req_nested","retry_after":42}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.RequestID != "req_body" || providerErr.RetryAfter != nil {
		t.Fatalf("anthropic body evidence = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesCompleteMid200AnthropicError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"type":"error","request_id":"req_mid_200","retry_after":11,` +
				`"error":{"type":"overloaded_error","message":"overloaded"}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.RequestID != "req_mid_200" || providerErr.RetryAfter != nil {
		t.Fatalf("mid-200 provider error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete mid-200 provider error must be explicit: %T %v", err, err)
	}
}
