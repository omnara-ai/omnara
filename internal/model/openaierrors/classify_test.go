package openaierrors

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
			name:       "server status beats quota prose",
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
			name:       "compatible maximum context prose",
			httpStatus: http.StatusBadRequest,
			message:    "This model's maximum context length is 131072 tokens.",
			codes:      []string{"invalid_request_error"},
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "compatible reduce messages prose",
			httpStatus: http.StatusBadRequest,
			message:    "Please reduce the length of the messages or completion.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "compatible prompt too long prose",
			httpStatus: http.StatusBadRequest,
			message:    "Prompt is too long for this model.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "compatible context window prose",
			httpStatus: http.StatusBadRequest,
			message:    "The combined input exceeds the context window.",
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "request entity too large is compactable payload overflow",
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
			if got := Classify(
				test.httpStatus,
				test.providerStatus,
				test.message,
				test.codes...,
			); got != test.want {
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
			name:    "verification marker",
			message: "The encrypted content could not be verified.",
			codes:   []string{"invalid_request_error"},
		},
		{
			name:    "decryption marker",
			message: "Encrypted content could not be decrypted or parsed.",
		},
		{
			name:    "decoding marker",
			message: "Encrypted function output content could not be decrypted or decoded.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Classify(http.StatusBadRequest, 0, test.message, test.codes...); got != model.ErrorKindReplayRejected {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindReplayRejected)
			}
		})
	}
	if got := Classify(
		http.StatusBadRequest,
		0,
		"The credential could not be decrypted.",
		"invalid_request_error",
	); got != model.ErrorKindInvalidRequest {
		t.Fatalf("unrelated decryption error = %q, want %q", got, model.ErrorKindInvalidRequest)
	}
}

func TestClassifyCurrentBillingCodes(t *testing.T) {
	for _, code := range []string{
		"credit_balance_exhausted",
		"organization_spend_limit_exceeded",
		"project_spend_limit_exceeded",
		"organization_usage_limit_exceeded",
	} {
		t.Run(code, func(t *testing.T) {
			if got := Classify(http.StatusTooManyRequests, 0, "limit reached", code); got != model.ErrorKindBillingAccount {
				t.Fatalf("Classify() = %q, want %q", got, model.ErrorKindBillingAccount)
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
	for _, test := range []struct {
		name           string
		httpStatus     int
		providerStatus int
		want           int
	}{
		{
			name:           "in-band provider error",
			httpStatus:     http.StatusOK,
			providerStatus: http.StatusBadGateway,
			want:           http.StatusBadGateway,
		},
		{
			name:           "transport error remains authoritative",
			httpStatus:     http.StatusServiceUnavailable,
			providerStatus: http.StatusBadRequest,
			want:           http.StatusServiceUnavailable,
		},
		{
			name:       "successful response without provider status",
			httpStatus: http.StatusOK,
			want:       http.StatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := EffectiveStatusCode(test.httpStatus, test.providerStatus); got != test.want {
				t.Fatalf("EffectiveStatusCode() = %d, want %d", got, test.want)
			}
		})
	}
}
