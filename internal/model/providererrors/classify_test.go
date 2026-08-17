package providererrors

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

func TestClassifyEvidencePrecedence(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		providerStatus int
		message        string
		codes          []string
		want           model.ErrorKind
	}{
		{
			name:       "exact billing code",
			httpStatus: http.StatusTooManyRequests,
			codes:      []string{"insufficient_quota"},
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
			codes:      []string{"invalid_request_error"},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "lossy Responses invalid prompt does not hide context prose",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"provider_unavailable", "invalid_prompt"},
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
			codes:      []string{"invalid_request_error"},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:           "context prose overrides generic OpenRouter gateway metadata",
			httpStatus:     http.StatusOK,
			providerStatus: http.StatusBadGateway,
			message:        "Your input exceeds the context window of this model.",
			codes:          []string{"provider_unavailable", "502"},
			want:           model.ErrorKindContextWindow,
		},
		{
			name:       "context prose overrides generic server code",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"server_error"},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "payload prose overrides generic gateway metadata",
			httpStatus: http.StatusBadGateway,
			message:    "The upstream rejected this request: request entity too large.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "payload prose overrides generic invalid request",
			httpStatus: http.StatusBadRequest,
			message:    "Payload too large for the provider endpoint.",
			codes:      []string{"invalid_request_error"},
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "redirect status rejects conflicting context prose",
			httpStatus: http.StatusTemporaryRedirect,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "auth status beats conflicting prose",
			httpStatus: http.StatusUnauthorized,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "auth status beats conflicting payload prose",
			httpStatus: http.StatusUnauthorized,
			message:    "Request body too large.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "billing status beats conflicting prose",
			httpStatus: http.StatusPaymentRequired,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"invalid_request_error"},
			want:       model.ErrorKindBillingAccount,
		},
		{
			name:       "rate status beats conflicting prose",
			httpStatus: http.StatusTooManyRequests,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "moderation status beats conflicting prose",
			httpStatus: http.StatusForbidden,
			message:    "Content policy: input exceeds the context window.",
			codes:      []string{"provider_unavailable"},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "permission wrapper allows moderation evidence",
			httpStatus: http.StatusForbidden,
			message:    "Request blocked by content policy.",
			codes:      []string{"permission_denied"},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "canonical content policy beats lossy server code",
			httpStatus: http.StatusBadGateway,
			codes:      []string{"content_policy_violation", "server_error"},
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "exact auth code beats conflicting prose",
			httpStatus: http.StatusBadGateway,
			message:    "Your input exceeds the context window of this model.",
			codes:      []string{"provider_unavailable", "invalid_api_key"},
			want:       model.ErrorKindAuth,
		},
		{
			name:       "first specific structured code is authoritative",
			httpStatus: http.StatusBadGateway,
			codes:      []string{"rate_limit_error", "context_length_exceeded"},
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "specific context code follows generic metadata",
			httpStatus: http.StatusBadGateway,
			codes:      []string{"provider_unavailable", "context_length_exceeded"},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "request entity too large is payload overflow",
			httpStatus: http.StatusRequestEntityTooLarge,
			message:    "Request exceeds maximum context length in bytes.",
			codes:      []string{"invalid_request_error"},
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
			if got := Classify(test.httpStatus, test.providerStatus, test.message, test.codes...); got != test.want {
				t.Fatalf("Classify() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyReplayRejection(t *testing.T) {
	for _, test := range []struct {
		name    string
		message string
		codes   []string
	}{
		{name: "structured code", codes: []string{"invalid_request_error", "invalid_encrypted_content"}},
		{
			name: "verification marker", message: "The encrypted content could not be verified.",
			codes: []string{"invalid_request_error"},
		},
		{name: "decryption marker", message: "Encrypted content could not be decrypted or parsed."},
		{name: "decoding marker", message: "Encrypted function output content could not be decrypted or decoded."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Classify(http.StatusBadRequest, 0, test.message, test.codes...); got != model.ErrorKindReplayRejected {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindReplayRejected)
			}
		})
	}
	got := Classify(
		http.StatusBadRequest,
		0,
		"The credential could not be decrypted.",
		"invalid_request_error",
	)
	if got != model.ErrorKindInvalidRequest {
		t.Fatalf("unrelated decryption error = %q, want %q", got, model.ErrorKindInvalidRequest)
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
			if got := Classify(http.StatusTooManyRequests, 0, "limit reached", code); got != model.ErrorKindBillingAccount {
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
			got := Classify(http.StatusBadGateway, 0, "provider detail", code, "server_error")
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
			if got := EffectiveStatusCode(test.httpStatus, test.providerStatus); got != test.want {
				t.Fatalf("EffectiveStatusCode() = %d, want %d", got, test.want)
			}
		})
	}
}
