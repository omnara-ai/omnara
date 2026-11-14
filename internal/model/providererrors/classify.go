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

func effectiveStatusCode(httpStatus, providerStatus int) int {
	if httpStatus >= 300 && httpStatus <= 599 {
		return httpStatus
	}
	if providerStatus >= http.StatusBadRequest && providerStatus <= 599 {
		return providerStatus
	}
	return httpStatus
}

type classification struct {
	kind    model.ErrorKind
	generic bool
}

// Specific evidence wins over generic wrappers. Transport status and precise
// upstream prose then refine those wrappers using the same policy for every API.
func classify(status int, message string, signals []classification, gatewayBilling bool) model.ErrorKind {
	genericKind := model.ErrorKindUnknown
	for _, signal := range signals {
		if signal.kind == "" || signal.kind == model.ErrorKindUnknown {
			continue
		}
		if !signal.generic {
			return signal.kind
		}
		if genericKind == model.ErrorKindUnknown {
			genericKind = signal.kind
		}
	}
	lower := normalizeMessage(message)
	switch status {
	case http.StatusUnauthorized:
		return model.ErrorKindAuth
	case http.StatusPaymentRequired:
		return model.ErrorKindBillingAccount
	case http.StatusRequestEntityTooLarge:
		return model.ErrorKindPayloadTooLarge
	case http.StatusTooManyRequests:
		if hasBillingMarker(lower) || gatewayBilling {
			return model.ErrorKindBillingAccount
		}
		return model.ErrorKindRateLimit
	case http.StatusBadRequest:
		if hasBillingMarker(lower) || gatewayBilling {
			return model.ErrorKindBillingAccount
		}
	case http.StatusForbidden:
		if hasBillingMarker(lower) || gatewayBilling {
			return model.ErrorKindBillingAccount
		}
		if hasModerationMarker(lower) {
			return model.ErrorKindInvalidRequest
		}
		return model.ErrorKindAuth
	}

	if hasReplayRejectionMarker(lower) && deterministicProseCanOverride(status, genericKind) {
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
	if hasBillingMarker(lower) || gatewayBilling {
		return model.ErrorKindBillingAccount
	}
	if strings.Contains(lower, "network_error") {
		return model.ErrorKindTransient
	}
	return model.ErrorKindUnknown
}

func deterministicProseCanOverride(status int, genericKind model.ErrorKind) bool {
	if !deterministicKindCanBeOverridden(genericKind) {
		return false
	}
	return status == 0 || (status >= http.StatusOK && status < http.StatusMultipleChoices) ||
		status == http.StatusBadRequest || status >= http.StatusInternalServerError
}

func deterministicKindCanBeOverridden(kind model.ErrorKind) bool {
	return kind == model.ErrorKindUnknown ||
		kind == model.ErrorKindProviderUnavailable ||
		kind == model.ErrorKindInvalidRequest
}
