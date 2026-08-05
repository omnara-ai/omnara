package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func (p protocol) errorSource() string {
	if strings.TrimSpace(p.client.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(p.client.BaseURL), "/")
	}
	return string(modelprotocol.APIFormatAnthropicMessages)
}

func (p protocol) invalidResponseError(
	resp route.Response,
	response messagesResponse,
	cause error,
) error {
	return model.AmbiguousProviderOutcome(model.ProviderError{
		Kind:       model.ErrorKindUnknown,
		Source:     p.errorSource(),
		StatusCode: resp.StatusCode,
		Code:       "malformed_success_response",
		Message:    cause.Error(),
		RequestID: firstAnthropicNonEmpty(
			model.RequestIDFromHeader(resp.Header),
			response.RequestID,
		),
		RetryAfter: model.RetryAfterFromHeader(resp.Header),
		Cause:      cause,
	})
}

func classifyErrorType(errorType, message string) model.ErrorKind {
	switch strings.ToLower(strings.TrimSpace(errorType)) {
	case "context_length_exceeded", "context_window_exceeded", "model_context_window_exceeded":
		return model.ErrorKindContextWindow
	case "request_too_large", "payload_too_large":
		return model.ErrorKindPayloadTooLarge
	case "authentication_error", "permission_error":
		return model.ErrorKindAuth
	case "billing_error", "payment_required":
		return model.ErrorKindBillingAccount
	case "rate_limit_error", "rate_limit_exceeded":
		return model.ErrorKindRateLimit
	case "timeout_error", "timeout":
		return model.ErrorKindTransient
	case "api_error", "overloaded_error", "provider_overloaded", "provider_unavailable", "server", "unmapped":
		return model.ErrorKindProviderUnavailable
	case "invalid_request_error":
		if anthropicContextWindowMessage(message) {
			return model.ErrorKindContextWindow
		}
		return model.ErrorKindInvalidRequest
	case "not_found_error", "invalid_request", "invalid_prompt", "max_tokens_exceeded",
		"token_limit_exceeded", "string_too_long", "unprocessable":
		return model.ErrorKindInvalidRequest
	default:
		return model.ErrorKindUnknown
	}
}

func anthropicContextWindowMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	message = strings.ReplaceAll(message, "`", "")
	return strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "input length and max_tokens exceed context limit")
}

type anthropicErrorEnvelope struct {
	Type      string             `json:"type"`
	Error     anthropicErrorBody `json:"error"`
	RequestID string             `json:"request_id"`
}

type anthropicErrorBody struct {
	Type      string `json:"type"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (e anthropicErrorBody) present() bool {
	return e.Type != "" || e.ErrorType != "" || e.Message != ""
}

func classifyHTTPError(source string, statusCode int, header http.Header, body []byte) model.ProviderError {
	payload := anthropicErrorEnvelope{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return anthropicProviderError(
			source,
			statusCode,
			header,
			anthropicErrorEnvelope{},
			strings.TrimSpace(string(body)),
		)
	}
	return anthropicProviderError(source, statusCode, header, payload, "")
}

func anthropicProviderError(
	source string,
	statusCode int,
	header http.Header,
	payload anthropicErrorEnvelope,
	fallbackMessage string,
) model.ProviderError {
	errorType := firstAnthropicNonEmpty(payload.Error.ErrorType, payload.Error.Type, payload.Type)
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = fallbackMessage
	}
	kind := classifyErrorType(errorType, message)
	if statusCode == http.StatusGatewayTimeout && kind == model.ErrorKindUnknown {
		kind = model.ErrorKindTransient
	} else if kind == model.ErrorKindUnknown {
		kind = model.ErrorKindFromHTTPStatus(statusCode)
	}
	return model.ProviderError{
		Kind:       kind,
		Source:     source,
		StatusCode: statusCode,
		Code:       errorType,
		Message:    message,
		RequestID: firstAnthropicNonEmpty(
			model.RequestIDFromHeader(header),
			payload.RequestID,
		),
		RetryAfter: model.RetryAfterFromHeader(header),
	}
}

func firstAnthropicNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
