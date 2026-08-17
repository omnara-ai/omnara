package providererrors

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

// Classify resolves provider error evidence in the authority order documented by
// the package. Specific structured codes are authoritative. Generic gateway or
// request wrappers remain fallback evidence so narrow deterministic prose can
// recover an upstream context overflow that the wrapper obscured.
func Classify(httpStatus, providerStatus int, message string, codes ...string) model.ErrorKind {
	genericKind := model.ErrorKindUnknown
	for _, value := range codes {
		kind, generic := classifyStructuredCode(value)
		if kind == model.ErrorKindUnknown {
			continue
		}
		if !generic {
			return kind
		}
		if genericKind == model.ErrorKindUnknown {
			genericKind = kind
		}
	}

	status := EffectiveStatusCode(httpStatus, providerStatus)
	lower := normalizeMessage(message)
	switch status {
	case http.StatusUnauthorized:
		return model.ErrorKindAuth
	case http.StatusPaymentRequired:
		return model.ErrorKindBillingAccount
	case http.StatusRequestEntityTooLarge:
		return model.ErrorKindPayloadTooLarge
	case http.StatusTooManyRequests:
		if hasBillingMarker(lower) {
			return model.ErrorKindBillingAccount
		}
		return model.ErrorKindRateLimit
	case http.StatusForbidden:
		if hasModerationMarker(lower) {
			return model.ErrorKindInvalidRequest
		}
		return model.ErrorKindAuth
	}

	if hasReplayRejectionMarker(lower) {
		return model.ErrorKindReplayRejected
	}
	if hasContextWindowMarker(lower) && deterministicProseCanOverride(status, genericKind) {
		return model.ErrorKindContextWindow
	}
	if hasPayloadTooLargeMarker(lower) && deterministicProseCanOverride(status, genericKind) {
		return model.ErrorKindPayloadTooLarge
	}
	if genericKind != model.ErrorKindUnknown {
		return genericKind
	}
	if kind := model.ErrorKindFromHTTPStatus(status); kind != model.ErrorKindUnknown {
		return kind
	}
	if hasBillingMarker(lower) {
		return model.ErrorKindBillingAccount
	}
	if strings.Contains(lower, "network_error") {
		return model.ErrorKindTransient
	}
	return model.ErrorKindUnknown
}

func classifyStructuredCode(value string) (model.ErrorKind, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "invalid_encrypted_content":
		return model.ErrorKindReplayRejected, false
	case "context_length_exceeded", "context_window_exceeded", "model_context_window_exceeded":
		return model.ErrorKindContextWindow, false
	case "payload_too_large", "request_too_large":
		return model.ErrorKindPayloadTooLarge, false
	case "insufficient_quota", "billing_hard_limit_reached", "billing_error", "payment_required",
		"credit_balance_exhausted", "organization_spend_limit_exceeded",
		"project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		return model.ErrorKindBillingAccount, false
	case "authentication", "authentication_error", "invalid_api_key":
		return model.ErrorKindAuth, false
	case "permission_error", "permission_denied":
		return model.ErrorKindAuth, true
	case "rate_limit_exceeded", "rate_limit_error":
		return model.ErrorKindRateLimit, false
	case "timeout", "timeout_error", "network_error":
		return model.ErrorKindTransient, false
	case "moderation", "content_filter", "content_policy", "content_policy_violation", "safety_policy", "refusal",
		"max_tokens_exceeded", "token_limit_exceeded", "string_too_long", "not_found", "not_found_error",
		"precondition_failed", "unprocessable", "invalid_image", "image_too_large", "image_too_small",
		"unsupported_image_format", "image_not_found", "image_download_failed":
		return model.ErrorKindInvalidRequest, false
	case "server", "server_error", "provider_error", "api_error", "provider_overloaded",
		"provider_unavailable", "overloaded_error", "unmapped":
		return model.ErrorKindProviderUnavailable, true
	case "invalid_request", "invalid_request_error", "invalid_prompt":
		return model.ErrorKindInvalidRequest, true
	default:
		return model.ErrorKindUnknown, false
	}
}

func deterministicProseCanOverride(status int, genericKind model.ErrorKind) bool {
	if genericKind != model.ErrorKindUnknown &&
		genericKind != model.ErrorKindProviderUnavailable &&
		genericKind != model.ErrorKindInvalidRequest {
		return false
	}
	return status == 0 || (status >= http.StatusOK && status < http.StatusMultipleChoices) ||
		status == http.StatusBadRequest || status >= http.StatusInternalServerError
}

func hasPayloadTooLargeMarker(message string) bool {
	return strings.Contains(message, "request entity too large") ||
		strings.Contains(message, "payload too large") ||
		strings.Contains(message, "request body too large") ||
		strings.Contains(message, "request too large") ||
		strings.Contains(message, "request_too_large")
}

func normalizeMessage(message string) string {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.ReplaceAll(message, "`", "")
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
		strings.Contains(message, "exceeds the context window") ||
		strings.Contains(message, "input length and max_tokens exceed context limit")
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
