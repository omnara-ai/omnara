package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

func TestRespondClassifiesProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_1")
		w.Header().Set("Retry-After", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_api_key","code":"invalid_api_key"}}`))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want model.ProviderError", err, err)
	}
	if providerErr.Kind != model.ErrorKindAuth ||
		providerErr.StatusCode != http.StatusUnauthorized ||
		providerErr.Code != "invalid_api_key" ||
		providerErr.RequestID != "req_1" ||
		providerErr.RetryAfter == nil ||
		providerErr.RetryAfter.HTTPDate == nil {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}

func TestRespondUnavailableStatusBeatsQuotaProse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"quota service temporarily unavailable"}}`))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable {
		t.Fatalf("conflicting status/message classification = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondDoesNotInferContextOverflowFromGenericBadRequestText(t *testing.T) {
	tests := []string{
		`{"error":{"message":"max tokens must be positive","type":"invalid_request_error"}}`,
		`{"error":{"message":"context parameter is malformed","code":"invalid_request"}}`,
		`{"error":{"message":"output token cap reached","metadata":{"error_type":"max_tokens_exceeded"}}}`,
	}
	for _, body := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		}))
		_, err := testRespondClient(server).Respond(
			context.Background(),
			model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
		)
		server.Close()
		providerErr, ok := model.ClassifyError(err)
		if !ok || providerErr.Kind != model.ErrorKindInvalidRequest {
			t.Fatalf("generic 400 classification = %+v ok=%v err=%v", providerErr, ok, err)
		}
	}
}

func TestRespondClassifiesMalformedHTTPErrorByStatus(t *testing.T) {
	tests := []struct {
		status int
		want   model.ErrorKind
	}{
		{status: http.StatusTooManyRequests, want: model.ErrorKindRateLimit},
		{status: http.StatusRequestEntityTooLarge, want: model.ErrorKindPayloadTooLarge},
		{status: http.StatusBadGateway, want: model.ErrorKindProviderUnavailable},
		{status: http.StatusTemporaryRedirect, want: model.ErrorKindInvalidRequest},
	}
	for _, tt := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(`{"error":`))
		}))
		_, err := testRespondClient(server).Respond(
			context.Background(),
			model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
		)
		server.Close()
		providerErr, ok := model.ClassifyError(err)
		if !ok || providerErr.Kind != tt.want || model.IsAmbiguousProviderOutcome(err) {
			t.Fatalf("status %d malformed error = %+v ok=%v err=%v", tt.status, providerErr, ok, err)
		}
	}
}

func TestRespondIgnoresUndocumentedBodyProviderEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(
			`{"request_id":"req_body","retry_after":31,` +
				`"error":{"message":"slow down","request_id":"req_error","retry_after":32,` +
				`"metadata":{"error_type":"rate_limit_exceeded","request_id":"req_metadata",` +
				`"retry_after":33}}}`,
		))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindRateLimit ||
		providerErr.RequestID != "" || providerErr.RetryAfter != nil {
		t.Fatalf("provider evidence = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesCompleteMid200OpenRouterError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"request_id":"req_mid_200","retry_after":19,` +
				`"error":{"code":502,"message":"upstream disconnected",` +
				`"metadata":{"error_type":"provider_unavailable"}}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.StatusCode != http.StatusBadGateway || providerErr.RequestID != "" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("mid-200 provider error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete mid-200 provider error must be explicit: %T %v", err, err)
	}
}

func TestRespondClassifiesOpenRouterWrappedContextOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(
			`{"error":{"code":502,"message":"Your input exceeds the context window of this model.",` +
				`"metadata":{"error_type":"provider_unavailable"}}}`,
		))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindContextWindow ||
		providerErr.StatusCode != http.StatusBadGateway ||
		providerErr.Code != "provider_unavailable" {
		t.Fatalf("wrapped context overflow = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesOpenRouterWrappedPayloadOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(
			`{"error":{"code":502,"message":"Request entity too large for the upstream provider.",` +
				`"metadata":{"error_type":"provider_unavailable"}}}`,
		))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindPayloadTooLarge ||
		providerErr.StatusCode != http.StatusBadGateway ||
		providerErr.Code != "provider_unavailable" {
		t.Fatalf("wrapped payload overflow = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesModerationForbiddenAsInvalidRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		body := `{"error":{"message":"Response blocked by moderation policy",` +
			`"type":"content_filter","code":"moderation"}}`
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want model.ProviderError", err, err)
	}
	if providerErr.Kind != model.ErrorKindInvalidRequest ||
		providerErr.StatusCode != http.StatusForbidden ||
		providerErr.Code != "moderation" {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}

func TestRespondClassifiesOpenRouterNumericErrors(t *testing.T) {
	body := `{"error":{"code":400,` +
		`"message":"This endpoint's maximum context length is 8192 tokens",` +
		`"metadata":{"error_type":"context_length_exceeded","provider_code":"bad_request"}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_openrouter")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want model.ProviderError", err, err)
	}
	if providerErr.Kind != model.ErrorKindContextWindow ||
		providerErr.StatusCode != http.StatusBadRequest ||
		providerErr.Code != "context_length_exceeded" ||
		providerErr.Message != "This endpoint's maximum context length is 8192 tokens" {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}

func TestRespondClassifiesOpenRouterChoiceErrors(t *testing.T) {
	body := `{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[` +
		`{"index":0,"message":{"role":"assistant"},"finish_reason":"error",` +
		`"error":{"code":502,"message":"Upstream provider overloaded",` +
		`"metadata":{"error_type":"provider_error","provider_code":"overloaded"}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_choice")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"anthropic/claude-sonnet-4"}`)},
	)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want model.ProviderError", err, err)
	}
	if providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.StatusCode != http.StatusBadGateway ||
		providerErr.Code != "provider_error" ||
		providerErr.Message != "Upstream provider overloaded" {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}

func TestRespondClassifiesOpenRouterMetadataErrorType(t *testing.T) {
	body := `{"error":{"message":"Token limit exceeded",` +
		`"metadata":{"error_type":"context_length_exceeded"}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want model.ProviderError", err, err)
	}
	if providerErr.Kind != model.ErrorKindContextWindow || providerErr.Code != "context_length_exceeded" {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}
