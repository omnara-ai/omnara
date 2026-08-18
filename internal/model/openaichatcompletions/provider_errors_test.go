package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

// https://github.com/OpenRouterTeam/ai-sdk-provider/issues/339
const openRouterFlatImmutableThinkingError = "{\"message\":\"messages.1.content.1: " +
	"`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. " +
	"These blocks must remain as they were in the original response.\"}"

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

func TestRespondClassifiesOpenRouterWrappedImmutableThinkingError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(
			`{"error":{"code":400,"message":"messages.1.content.0: ` +
				"`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. " +
				`These blocks must remain as they were in the original response.",` +
				`"metadata":{"error_type":"invalid_request_error"}}}`,
		))
	}))
	defer server.Close()
	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindReplayRejected ||
		providerErr.StatusCode != http.StatusBadRequest ||
		providerErr.Code != "invalid_request_error" {
		t.Fatalf("wrapped immutable thinking error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesOpenRouterRawReplayError(t *testing.T) {
	for _, test := range []struct {
		name        string
		raw         any
		wantCode    string
		wantMessage string
	}{
		{
			name: "wrapped error",
			raw: json.RawMessage(`{"type":"error","error":{"type":"invalid_request_error",` +
				"\"message\":\"Invalid `signature` in `thinking` block\"}}"),
			wantCode:    "invalid_request_error",
			wantMessage: "Invalid `signature` in `thinking` block",
		},
		{
			name:     "flat error",
			raw:      openRouterFlatImmutableThinkingError,
			wantCode: "400",
			wantMessage: "messages.1.content.1: `thinking` or `redacted_thinking` blocks in the latest " +
				"assistant message cannot be modified. These blocks must remain as they were in the original response.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"error": map[string]any{
					"code":    400,
					"message": "Provider returned error",
					"metadata": map[string]any{
						"provider_name": "Anthropic",
						"raw":           test.raw,
					},
				},
			})
			if err != nil {
				t.Fatalf("marshal raw provider error: %v", err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			client := testRespondClient(server)
			client.APIVariant = modelprotocol.APIVariantOpenRouter
			_, err = client.Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != model.ErrorKindReplayRejected ||
				providerErr.StatusCode != http.StatusBadRequest ||
				providerErr.Code != test.wantCode || providerErr.Message != test.wantMessage {
				t.Fatalf("raw replay error = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestRespondOnlyUsesRawProviderErrorsForOpenRouter(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    400,
			"message": "Provider returned error",
			"metadata": map[string]any{
				"raw": `{"type":"error","error":{"type":"invalid_request_error",` +
					"\"message\":\"Invalid `signature` in `thinking` block\"}}",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal nested provider error: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	_, err = testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindInvalidRequest ||
		providerErr.Code != "400" || providerErr.Message != "Provider returned error" {
		t.Fatalf("default-variant provider error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClassifyProviderErrorBoundsRawEvidence(t *testing.T) {
	nested, err := json.Marshal(`{"type":"error","error":{"type":"invalid_request_error",` +
		"\"message\":\"Invalid `signature` in `thinking` block\"}}")
	if err != nil {
		t.Fatalf("marshal nested error: %v", err)
	}
	oversized, err := json.Marshal(strings.Repeat("x", maxNestedProviderErrorBytes+1))
	if err != nil {
		t.Fatalf("marshal oversized nested error: %v", err)
	}
	for _, test := range []struct {
		name        string
		httpStatus  int
		providerErr chatProviderError
		wantKind    model.ErrorKind
		wantCode    string
		wantMessage string
	}{
		{
			name: "specific outer error",
			providerErr: chatProviderError{
				Message:  "API key is invalid",
				Type:     "invalid_api_key",
				Metadata: chatProviderErrorMetadata{Raw: nested},
			},
			wantKind:    model.ErrorKindAuth,
			wantCode:    "invalid_api_key",
			wantMessage: "API key is invalid",
		},
		{
			name: "specific outer code with generic message",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "invalid_api_key",
				Metadata: chatProviderErrorMetadata{Raw: nested},
			},
			wantKind:    model.ErrorKindAuth,
			wantCode:    "invalid_api_key",
			wantMessage: "Provider returned error",
		},
		{
			name: "specific outer type with numeric code",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "invalid_api_key",
				Code:     400,
				Metadata: chatProviderErrorMetadata{Raw: nested},
			},
			wantKind:    model.ErrorKindAuth,
			wantCode:    "400",
			wantMessage: "Provider returned error",
		},
		{
			name: "permission outer type with numeric code",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "permission_denied",
				Code:     400,
				Metadata: chatProviderErrorMetadata{Raw: nested},
			},
			wantKind:    model.ErrorKindAuth,
			wantCode:    "400",
			wantMessage: "Provider returned error",
		},
		{
			name:       "masked server error",
			httpStatus: http.StatusBadGateway,
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Code:     502,
				Metadata: chatProviderErrorMetadata{Raw: nested},
			},
			wantKind:    model.ErrorKindProviderUnavailable,
			wantCode:    "502",
			wantMessage: "Provider returned error",
		},
		{
			name: "malformed nested error",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "provider_error",
				Metadata: chatProviderErrorMetadata{Raw: json.RawMessage(`"{"`)},
			},
			wantKind:    model.ErrorKindProviderUnavailable,
			wantMessage: "Provider returned error",
		},
		{
			name: "oversized nested error",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "provider_error",
				Metadata: chatProviderErrorMetadata{Raw: oversized},
			},
			wantKind:    model.ErrorKindProviderUnavailable,
			wantMessage: "Provider returned error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusCode := test.httpStatus
			if statusCode == 0 {
				statusCode = http.StatusBadRequest
			}
			got := classifyProviderError(
				"test-provider",
				modelprotocol.APIVariantOpenRouter,
				statusCode,
				nil,
				test.providerErr,
				"",
			)
			if got.Kind != test.wantKind || (test.wantCode != "" && got.Code != test.wantCode) ||
				got.Message != test.wantMessage {
				t.Fatalf(
					"provider error = %+v, want kind %q code %q message %q",
					got, test.wantKind, test.wantCode, test.wantMessage,
				)
			}
		})
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
