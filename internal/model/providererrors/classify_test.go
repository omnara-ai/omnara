package providererrors

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestClassifyEvidencePrecedence(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		providerStatus int
		message        string
		signals        []classification
		gatewayBilling bool
		want           model.ErrorKind
	}{
		{
			name:       "exact billing code",
			httpStatus: http.StatusTooManyRequests,
			signals:    []classification{classifyOpenAICode("insufficient_quota")},
			want:       model.ErrorKindBillingAccount,
		},
		{
			name:       "server status beats weak quota prose",
			httpStatus: http.StatusServiceUnavailable,
			message:    "quota service temporarily unavailable",
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:           "body status fills successful HTTP envelope",
			httpStatus:     http.StatusOK,
			providerStatus: http.StatusBadGateway,
			message:        "upstream disconnected",
			want:           model.ErrorKindProviderUnavailable,
		},
		{
			name:           "authoritative HTTP status beats conflicting body status",
			httpStatus:     http.StatusServiceUnavailable,
			providerStatus: http.StatusBadRequest,
			want:           model.ErrorKindProviderUnavailable,
		},
		{
			name:       "standard quota prose on 429",
			httpStatus: http.StatusTooManyRequests,
			message:    "Please check your plan and billing details.",
			want:       model.ErrorKindBillingAccount,
		},
		{
			name:       "maximum context prose behind invalid request",
			httpStatus: http.StatusBadRequest,
			message:    "This model's maximum context length is 131072 tokens.",
			signals:    []classification{classifyOpenAICode("invalid_request_error")},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "lossy Responses invalid prompt does not hide context prose",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable"), classifyOpenAICode("invalid_prompt")},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "reduce messages prose",
			httpStatus: http.StatusBadRequest,
			message:    "Please reduce the length of the messages or completion.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "prompt too long prose",
			httpStatus: http.StatusBadRequest,
			message:    "Prompt is too long for this model.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "context window prose",
			httpStatus: http.StatusBadRequest,
			message:    "The combined input exceeds the context window.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "Anthropic context prose",
			httpStatus: http.StatusBadRequest,
			message:    "input length and `max_tokens` exceed context limit",
			signals:    []classification{classifyOpenAICode("invalid_request_error")},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:           "context prose overrides generic OpenRouter gateway metadata",
			httpStatus:     http.StatusOK,
			providerStatus: http.StatusBadGateway,
			message:        "Your input exceeds the context window of this model.",
			signals:        []classification{classifyOpenRouterType("provider_unavailable"), classifyOpenRouterType("502")},
			want:           model.ErrorKindContextWindow,
		},
		{
			name:       "context prose overrides generic server code",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenAICode("server_error")},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "payload prose overrides generic gateway metadata",
			httpStatus: http.StatusBadGateway,
			message:    "The upstream rejected this request: request entity too large.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "payload prose overrides generic invalid request",
			httpStatus: http.StatusBadRequest,
			message:    "Payload too large for the provider endpoint.",
			signals:    []classification{classifyOpenAICode("invalid_request_error")},
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "redirect status rejects conflicting context prose",
			httpStatus: http.StatusTemporaryRedirect,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "auth status beats conflicting prose",
			httpStatus: http.StatusUnauthorized,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "auth status beats conflicting payload prose",
			httpStatus: http.StatusUnauthorized,
			message:    "Request body too large.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "billing status beats conflicting prose",
			httpStatus: http.StatusPaymentRequired,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenAICode("invalid_request_error")},
			want:       model.ErrorKindBillingAccount,
		},
		{
			name:       "rate status beats conflicting prose",
			httpStatus: http.StatusTooManyRequests,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "moderation status beats conflicting prose",
			httpStatus: http.StatusForbidden,
			message:    "Content policy: input exceeds the context window.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable")},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:           "budget limit status is billing",
			gatewayBilling: true,
			httpStatus:     http.StatusForbidden,
			message:        "Org member budget limit exceeded (weekly limit). Contact your org admin.",
			want:           model.ErrorKindBillingAccount,
		},
		{
			name:       "permission wrapper allows moderation evidence",
			httpStatus: http.StatusForbidden,
			message:    "Request blocked by content policy.",
			signals:    []classification{classifyOpenRouterType("permission_denied")},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "canonical content policy beats lossy server code",
			httpStatus: http.StatusBadGateway,
			signals:    []classification{classifyOpenRouterType("content_policy_violation"), classifyOpenAICode("server_error")},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "exact auth code beats conflicting prose",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			signals:    []classification{classifyOpenRouterType("provider_unavailable"), classifyOpenAICode("invalid_api_key")},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "first specific structured code is authoritative",
			httpStatus: http.StatusBadGateway,
			signals: []classification{
				classifyAnthropicCode("rate_limit_error"), classifyOpenAICode("context_length_exceeded"),
			},
			want: model.ErrorKindRateLimit,
		},
		{
			name:       "specific context code follows generic metadata",
			httpStatus: http.StatusBadGateway,
			signals: []classification{
				classifyOpenRouterType("provider_unavailable"), classifyOpenAICode("context_length_exceeded"),
			},
			want: model.ErrorKindContextWindow,
		},
		{
			name:       "request entity too large is payload overflow",
			httpStatus: http.StatusRequestEntityTooLarge,
			message:    "Request exceeds maximum context length in bytes.",
			signals:    []classification{classifyOpenAICode("invalid_request_error")},
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "generic token prose is not context overflow",
			httpStatus: http.StatusBadRequest,
			message:    "max_tokens is invalid",
			want:       model.ErrorKindInvalidRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := effectiveStatusCode(test.httpStatus, test.providerStatus)
			if got := classify(status, test.message, test.signals, test.gatewayBilling); got != test.want {
				t.Fatalf("Classify() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyReplayRejection(t *testing.T) {
	for _, test := range []struct {
		name    string
		message string
		signals []classification
	}{
		{
			name: "structured code",
			signals: []classification{
				classifyOpenAICode("invalid_request_error"), classifyOpenAICode("invalid_encrypted_content"),
			},
		},
		{
			name: "verification marker", message: "The encrypted content could not be verified.",
			signals: []classification{classifyOpenAICode("invalid_request_error")},
		},
		{name: "decryption marker", message: "Encrypted content could not be decrypted or parsed."},
		{name: "decoding marker", message: "Encrypted function output content could not be decrypted or decoded."},
		{
			name: "Anthropic immutable thinking",
			message: "messages.1.content.0: `thinking` or `redacted_thinking` blocks in the latest " +
				"assistant message cannot be modified. These blocks must remain as they were in the original response.",
			signals: []classification{classifyOpenAICode("invalid_request_error")},
		},
		{
			name:    "Anthropic invalid thinking signature",
			message: "Invalid `signature` in `thinking` block",
			signals: []classification{classifyOpenAICode("invalid_request_error")},
		},
		{
			name: "Anthropic preserved thinking bound to a different conversation",
			message: "messages.5.content.0: Invalid `signature` in `thinking` block. The block is bound to a " +
				"different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` " +
				"to \"drop_block\". That setting requires the `thinking-binding-controls-2026-08-01` value in the " +
				"`anthropic-beta` header.",
			signals: []classification{classifyOpenAICode("invalid_request_error")},
		},
		{
			name:    "Anthropic-compatible invalid thinking signature",
			message: "messages.1.content.0: Invalid signature in thinking",
			signals: []classification{classifyOpenAICode("invalid_request_error")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classify(http.StatusBadRequest, test.message, test.signals, false); got != model.ErrorKindReplayRejected {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindReplayRejected)
			}
		})
	}
	for _, message := range []string{
		"The credential could not be decrypted.",
		"The request signature is invalid.",
		"The assistant message cannot be modified.",
		"The thinking setting in the latest assistant message cannot be modified.",
	} {
		got := classify(http.StatusBadRequest, message, []classification{classifyOpenAICode("invalid_request_error")}, false)
		if got != model.ErrorKindInvalidRequest {
			t.Fatalf("unrelated message %q = %q, want %q", message, got, model.ErrorKindInvalidRequest)
		}
	}
	got := classify(
		http.StatusUnauthorized, "Invalid signature in thinking block",
		[]classification{classifyOpenAICode("invalid_api_key")}, false,
	)
	if got != model.ErrorKindAuth {
		t.Fatalf("auth evidence with replay prose = %q, want %q", got, model.ErrorKindAuth)
	}
	for _, test := range []struct {
		name       string
		httpStatus int
		signals    []classification
		want       model.ErrorKind
	}{
		{
			name:       "not found status",
			httpStatus: http.StatusNotFound,
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "permission evidence",
			httpStatus: http.StatusBadRequest,
			signals:    []classification{classifyOpenRouterType("permission_denied")},
			want:       model.ErrorKindAuth,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classify(test.httpStatus, "Invalid signature in thinking block", test.signals, false)
			if got != test.want {
				t.Fatalf("conflicting replay prose = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyCurrentBillingCodes(t *testing.T) {
	codes := []string{
		"credit_balance_exhausted",
		"organization_spend_limit_exceeded",
		"project_spend_limit_exceeded",
		"organization_usage_limit_exceeded",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			got := Interpret(modelprotocol.APIVariantDefault, Evidence{
				HTTPStatus: http.StatusTooManyRequests, Message: "limit reached", Native: OpenAI(code, ""),
			}).Kind
			if got != model.ErrorKindBillingAccount {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindBillingAccount)
			}
		})
	}
}

func TestClassifyCurrentOpenRouterCanonicalRequestCodes(t *testing.T) {
	codes := []string{
		"content_policy_violation",
		"refusal",
		"not_found",
		"precondition_failed",
		"unprocessable",
		"invalid_image",
		"image_too_large",
		"image_too_small",
		"unsupported_image_format",
		"image_not_found",
		"image_download_failed",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			got := Interpret(modelprotocol.APIVariantOpenRouter, Evidence{
				HTTPStatus: http.StatusBadGateway, Message: "provider detail",
				Native: OpenAI("server_error", ""), OpenRouter: openRouterEvidence(t, openRouterError{ErrorType: code}),
			}).Kind
			if got != model.ErrorKindInvalidRequest {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindInvalidRequest)
			}
		})
	}
}

func TestStructuredCodeNormalization(t *testing.T) {
	for _, test := range []struct {
		value any
		text  string
		code  int
	}{
		{value: " 429 ", text: "429", code: http.StatusTooManyRequests},
		{value: float64(503), text: "503", code: http.StatusServiceUnavailable},
		{value: float64(429.5), text: "429.5"},
		{value: map[string]any{"unexpected": true}},
	} {
		if got := CodeText(test.value); got != test.text {
			t.Fatalf("CodeText(%v) = %q, want %q", test.value, got, test.text)
		}
		if got := StatusCode(test.value); got != test.code {
			t.Fatalf("StatusCode(%v) = %d, want %d", test.value, got, test.code)
		}
	}
}

func TestEffectiveStatusCode(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		providerStatus int
		want           int
	}{
		{
			name: "in-band provider error", httpStatus: http.StatusOK,
			providerStatus: http.StatusBadGateway, want: http.StatusBadGateway,
		},
		{
			name: "transport error remains authoritative", httpStatus: http.StatusServiceUnavailable,
			providerStatus: http.StatusBadRequest, want: http.StatusServiceUnavailable,
		},
		{name: "successful response without provider status", httpStatus: http.StatusOK, want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveStatusCode(test.httpStatus, test.providerStatus); got != test.want {
				t.Fatalf("effectiveStatusCode() = %d, want %d", got, test.want)
			}
		})
	}
}
