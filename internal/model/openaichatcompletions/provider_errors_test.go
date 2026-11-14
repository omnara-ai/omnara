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

type openRouterRawReplayErrorCase struct {
	name        string
	raw         any
	wantCode    string
	wantMessage string
}

func openRouterRawReplayErrorCases() []openRouterRawReplayErrorCase {
	return []openRouterRawReplayErrorCase{
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
		{
			name:        "plain string",
			raw:         "Invalid signature in thinking block",
			wantCode:    "400",
			wantMessage: "Invalid signature in thinking block",
		},
		{
			name:        "detail field",
			raw:         map[string]any{"detail": "Invalid signature in thinking block"},
			wantCode:    "400",
			wantMessage: "Invalid signature in thinking block",
		},
	}
}

func TestRespondClassifiesProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req_1")
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

	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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

func TestRespondClassifiesOpenRouterKeyLimitAsBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(
			`{"error":{"code":403,"message":"Key limit exceeded (total limit). Manage it using ` +
				`https://openrouter.ai/workspaces/default/keys/abc $10.0 of $10.0 used; does not reset."}}`,
		))
	}))
	defer server.Close()
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindBillingAccount || providerErr.StatusCode != http.StatusForbidden {
		t.Fatalf("OpenRouter key limit = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondClassifiesOpenRouterInFlightBudgetAsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(
			`{"error":{"code":"payment_required","message":"This request would exceed your available credits ` +
				`given your current in-flight requests.","metadata":{"reason":"in_flight_budget_exhausted",` +
				`"limit_source":"openrouter_in_flight_budget"}}}`,
		))
	}))
	defer server.Close()
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindTransient || providerErr.RetryAfter == nil {
		t.Fatalf("OpenRouter in-flight budget = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClassifyOpenRouterLimitErrors(t *testing.T) {
	const spent = "Limit reached. $10.00 of $10.00 used; does not reset."
	const refusal = "This request requires more credits, or fewer max_tokens. " +
		"You requested up to 128000 tokens, but can only afford 40000."
	for _, test := range []struct {
		name           string
		variant        modelprotocol.APIVariant
		status         int
		limitSource    string
		message        string
		wantKind       model.ErrorKind
		wantAffordable *int
	}{
		{"key limit", modelprotocol.APIVariantOpenRouter, http.StatusForbidden,
			"openrouter_key_limit", spent, model.ErrorKindBillingAccount, nil},
		{"workspace budget", modelprotocol.APIVariantOpenRouter, http.StatusForbidden,
			"openrouter_workspace_budget", spent, model.ErrorKindBillingAccount, nil},
		{"guardrail budget", modelprotocol.APIVariantOpenRouter, http.StatusForbidden,
			"openrouter_guardrail_budget", spent, model.ErrorKindBillingAccount, nil},
		{"limit source ignored without OpenRouter variant", modelprotocol.APIVariantDefault, http.StatusForbidden,
			"openrouter_key_limit", spent, model.ErrorKindAuth, nil},
		{"affordable output", modelprotocol.APIVariantOpenRouter, http.StatusPaymentRequired,
			"", refusal, model.ErrorKindBillingAccount, new(40000)},
		{"affordable output ignored without OpenRouter variant", modelprotocol.APIVariantDefault,
			http.StatusPaymentRequired, "", refusal, model.ErrorKindBillingAccount, nil},
		{"billing without amount", modelprotocol.APIVariantOpenRouter, http.StatusPaymentRequired,
			"", "Insufficient credits", model.ErrorKindBillingAccount, nil},
		{"amount ignored on non-billing errors", modelprotocol.APIVariantOpenRouter, http.StatusBadRequest,
			"", "can only afford 40000", model.ErrorKindInvalidRequest, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyProviderError(
				"test-provider",
				test.variant,
				test.status,
				nil,
				chatProviderError{
					Message:  test.message,
					Metadata: openRouterMetadataFixture(t, map[string]any{"limit_source": test.limitSource}),
				},
				"",
			)
			if got.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q (%+v)", got.Kind, test.wantKind, got)
			}
			if (got.AffordableOutputTokens == nil) != (test.wantAffordable == nil) ||
				got.AffordableOutputTokens != nil && *got.AffordableOutputTokens != *test.wantAffordable {
				t.Fatalf("affordable output tokens = %v, want %v", got.AffordableOutputTokens, test.wantAffordable)
			}
		})
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
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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
	for _, test := range openRouterRawReplayErrorCases() {
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
	oversized, err := json.Marshal(strings.Repeat("x", 64*1024+1))
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
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": nested}),
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
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": nested}),
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
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": nested}),
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
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": nested}),
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
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": nested}),
			},
			wantKind:    model.ErrorKindProviderUnavailable,
			wantCode:    "502",
			wantMessage: "Provider returned error",
		},
		{
			name: "invalid raw JSON",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "provider_error",
				Metadata: json.RawMessage(`{`),
			},
			wantKind:    model.ErrorKindProviderUnavailable,
			wantMessage: "Provider returned error",
		},
		{
			name: "oversized nested error",
			providerErr: chatProviderError{
				Message:  "Provider returned error",
				Type:     "provider_error",
				Metadata: openRouterMetadataFixture(t, map[string]any{"raw": oversized}),
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
		w.Header().Set("X-Request-ID", "req_openrouter")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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
		w.Header().Set("X-Request-ID", "req_choice")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.Respond(
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

func openRouterMetadataFixture(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
