package providererrors

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

// https://platform.claude.com/docs/en/api/errors
// https://platform.claude.com/docs/en/api/rate-limits#reaching-your-spend-cap
func classifyAnthropicCode(value string) classification {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "request_too_large":
		return classification{kind: model.ErrorKindPayloadTooLarge}
	case "billing_error", "enforced_spend_limit_reached":
		return classification{kind: model.ErrorKindBillingAccount}
	case "authentication_error":
		return classification{kind: model.ErrorKindAuth}
	case "permission_error":
		return classification{kind: model.ErrorKindAuth, generic: true}
	case "rate_limit_error":
		return classification{kind: model.ErrorKindRateLimit}
	case "timeout_error":
		return classification{kind: model.ErrorKindTransient}
	case "not_found_error":
		return classification{kind: model.ErrorKindInvalidRequest}
	case "api_error", "overloaded_error":
		return classification{kind: model.ErrorKindProviderUnavailable, generic: true}
	case "invalid_request_error":
		return classification{kind: model.ErrorKindInvalidRequest, generic: true}
	default:
		return classifyCompatibleCode(value)
	}
}
