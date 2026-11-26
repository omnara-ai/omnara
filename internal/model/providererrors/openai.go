package providererrors

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

// OpenAI's Chat Completions and Responses share this native error vocabulary.
// https://developers.openai.com/api/docs/guides/error-codes
// https://developers.openai.com/api/reference/resources/responses/methods/retrieve
func classifyOpenAICode(value string) classification {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "invalid_encrypted_content":
		return classification{kind: model.ErrorKindReplayRejected}
	case "context_length_exceeded":
		return classification{kind: model.ErrorKindContextWindow}
	case "insufficient_quota", "billing_hard_limit_reached", "credit_balance_exhausted",
		"organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		return classification{kind: model.ErrorKindBillingAccount}
	case "invalid_api_key":
		return classification{kind: model.ErrorKindAuth}
	case "rate_limit_exceeded", "rate_limit_error", "slow_down":
		return classification{kind: model.ErrorKindRateLimit}
	case "vector_store_timeout":
		return classification{kind: model.ErrorKindTransient}
	case "content_filter", "data_residency_mismatch", "bio_policy", "misalignment_policy_violation",
		"invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url",
		"image_too_large", "image_too_small", "image_parse_error", "image_content_policy_violation",
		"invalid_image_mode", "image_file_too_large", "unsupported_image_media_type", "empty_image_file",
		"failed_to_download_image", "image_file_not_found":
		return classification{kind: model.ErrorKindInvalidRequest}
	case "server_error", "service_unavailable_error", "server_is_overloaded":
		return classification{kind: model.ErrorKindProviderUnavailable, generic: true}
	case "invalid_request_error", "invalid_prompt":
		return classification{kind: model.ErrorKindInvalidRequest, generic: true}
	default:
		return classifyCompatibleCode(value)
	}
}
