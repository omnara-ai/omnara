package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
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

type anthropicErrorEnvelope struct {
	Type      string             `json:"type"`
	Error     anthropicErrorBody `json:"error"`
	RequestID string             `json:"request_id"`
}

type anthropicErrorBody struct {
	Type      string          `json:"type"`
	ErrorType json.RawMessage `json:"error_type"`
	Message   string          `json:"message"`
	Details   struct {
		ErrorCode string `json:"error_code"`
	} `json:"details"`
}

func (e anthropicErrorBody) present() bool {
	return e.Type != "" || e.Message != "" || providererrors.OpenRouterType(e.ErrorType).Present()
}

func classifyHTTPError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	body []byte,
) model.ProviderError {
	payload := anthropicErrorEnvelope{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return anthropicProviderError(
			source,
			variant,
			statusCode,
			header,
			anthropicErrorEnvelope{},
			strings.TrimSpace(string(body)),
		)
	}
	return anthropicProviderError(source, variant, statusCode, header, payload, strings.TrimSpace(string(body)))
}

func anthropicProviderError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	payload anthropicErrorEnvelope,
	fallbackMessage string,
) model.ProviderError {
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = fallbackMessage
	}
	result := providererrors.Interpret(variant, providererrors.Evidence{
		HTTPStatus: statusCode, Message: message,
		Native: providererrors.Anthropic(
			firstAnthropicNonEmpty(payload.Error.Type, payload.Type), payload.Error.Details.ErrorCode,
		),
		OpenRouter: providererrors.OpenRouterType(payload.Error.ErrorType),
	})
	result.Source = source
	result.RequestID = firstAnthropicNonEmpty(model.RequestIDFromHeader(header), payload.RequestID)
	result.RetryAfter = model.RetryAfterFromHeader(header)
	return result
}

func firstAnthropicNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
