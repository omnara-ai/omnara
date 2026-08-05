package openaierrors

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

func CodeText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func StatusCode(values ...any) int {
	for _, value := range values {
		parsed, err := strconv.Atoi(CodeText(value))
		if err == nil && parsed >= http.StatusBadRequest && parsed <= 599 {
			return parsed
		}
	}
	return 0
}

func EffectiveStatusCode(httpStatus, providerStatus int) int {
	if httpStatus >= 300 && httpStatus <= 599 {
		return httpStatus
	}
	if providerStatus >= http.StatusBadRequest && providerStatus <= 599 {
		return providerStatus
	}
	return httpStatus
}

func Classify(httpStatus, providerStatus int, message string, codes ...string) model.ErrorKind {
	structuredInvalidRequest := false
	for _, value := range codes {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "invalid_encrypted_content":
			return model.ErrorKindReplayRejected
		case "context_length_exceeded", "context_window_exceeded", "model_context_window_exceeded":
			return model.ErrorKindContextWindow
		case "payload_too_large", "request_too_large":
			return model.ErrorKindPayloadTooLarge
		case "insufficient_quota", "billing_hard_limit_reached", "billing_error", "payment_required",
			"credit_balance_exhausted", "organization_spend_limit_exceeded",
			"project_spend_limit_exceeded", "organization_usage_limit_exceeded":
			return model.ErrorKindBillingAccount
		case "authentication", "authentication_error", "invalid_api_key", "permission_denied":
			return model.ErrorKindAuth
		case "rate_limit_exceeded", "rate_limit_error":
			return model.ErrorKindRateLimit
		case "timeout", "timeout_error", "network_error":
			return model.ErrorKindTransient
		case "server", "server_error", "provider_error", "provider_overloaded", "provider_unavailable",
			"overloaded_error", "unmapped":
			return model.ErrorKindProviderUnavailable
		case "moderation", "content_filter", "content_policy", "safety_policy", "invalid_prompt",
			"invalid_request", "invalid_request_error", "max_tokens_exceeded", "token_limit_exceeded",
			"string_too_long", "unprocessable":
			structuredInvalidRequest = true
		}
	}

	status := EffectiveStatusCode(httpStatus, providerStatus)
	lower := strings.ToLower(strings.TrimSpace(message))
	if hasReplayRejectionMarker(lower) {
		return model.ErrorKindReplayRejected
	}
	if status == http.StatusTooManyRequests && hasBillingMarker(lower) {
		return model.ErrorKindBillingAccount
	}
	if status == http.StatusForbidden && hasModerationMarker(lower) {
		return model.ErrorKindInvalidRequest
	}
	if status == http.StatusRequestEntityTooLarge {
		return model.ErrorKindPayloadTooLarge
	}
	if hasContextWindowMarker(lower) {
		return model.ErrorKindContextWindow
	}
	if structuredInvalidRequest {
		return model.ErrorKindInvalidRequest
	}

	if kind := model.ErrorKindFromHTTPStatus(status); kind != model.ErrorKindUnknown {
		return kind
	}
	switch {
	case hasBillingMarker(lower):
		return model.ErrorKindBillingAccount
	case strings.Contains(lower, "network_error"):
		return model.ErrorKindTransient
	default:
		return model.ErrorKindUnknown
	}
}

func hasReplayRejectionMarker(message string) bool {
	encryptedContent := strings.Contains(message, "encrypted content") ||
		strings.Contains(message, "encrypted function output content")
	return encryptedContent &&
		(strings.Contains(message, "could not be verified") ||
			strings.Contains(message, "could not be decrypted") ||
			strings.Contains(message, "could not be decoded"))
}

func hasContextWindowMarker(message string) bool {
	return strings.Contains(message, "maximum context length") ||
		strings.Contains(message, "reduce the length of the messages") ||
		strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "exceeds the context window")
}

func hasBillingMarker(message string) bool {
	return strings.Contains(message, "insufficient_quota") ||
		strings.Contains(message, "insufficient quota") ||
		strings.Contains(message, "billing_hard_limit") ||
		strings.Contains(message, "billing hard limit") ||
		strings.Contains(message, "please check your plan and billing details")
}

func hasModerationMarker(message string) bool {
	return strings.Contains(message, "moderation") ||
		strings.Contains(message, "content_filter") ||
		strings.Contains(message, "content filter") ||
		strings.Contains(message, "content policy") ||
		strings.Contains(message, "safety policy")
}
