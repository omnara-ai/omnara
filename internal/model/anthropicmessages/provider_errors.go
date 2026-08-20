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
	kind := providererrors.Classify(
		statusCode,
		0,
		message,
		payload.Error.ErrorType,
		payload.Error.Type,
		payload.Type,
	)
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
