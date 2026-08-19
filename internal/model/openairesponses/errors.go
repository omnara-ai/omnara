package openairesponses

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
	return string(modelprotocol.APIFormatOpenAIResponses)
}

func (p protocol) invalidResponseError(
	resp route.Response,
	response responsesResponse,
	cause error,
) error {
	return model.AmbiguousProviderOutcome(model.ProviderError{
		Kind:       model.ErrorKindUnknown,
		Source:     p.errorSource(),
		StatusCode: resp.StatusCode,
		Code:       "malformed_success_response",
		Message:    cause.Error(),
		RequestID:  model.RequestIDFromHeader(resp.Header),
		RetryAfter: model.RetryAfterFromHeader(resp.Header),
		Cause:      cause,
	})
}

func responseFailureError(
	source string,
	response responsesResponse,
	statusCode int,
	header http.Header,
) model.ProviderError {
	message := response.Error.Message
	if message == "" {
		message = "openai-responses request failed"
	}
	code := firstNonEmpty(response.ErrorType, response.Error.codeText(), response.Error.Type)
	return model.ProviderError{
		Kind: classifyResponsesError(
			statusCode,
			0,
			message,
			response.ErrorType,
			response.Error.codeText(),
			response.Error.Type,
		),
		Source:     source,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
	}
}

func classifyHTTPError(source string, statusCode int, header http.Header, body []byte) model.ProviderError {
	message := strings.TrimSpace(string(body))
	var envelope struct {
		ErrorType string         `json:"error_type"`
		Error     responsesError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return model.ProviderError{
			Kind:       classifyResponsesError(statusCode, 0, message),
			Source:     source,
			StatusCode: statusCode,
			Message:    message,
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
		}
	}
	if envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	code := firstNonEmpty(envelope.ErrorType, envelope.Error.codeText(), envelope.Error.Type)
	return model.ProviderError{
		Kind: classifyResponsesError(
			statusCode,
			0,
			message,
			envelope.ErrorType,
			envelope.Error.codeText(),
			envelope.Error.Type,
		),
		Source:     source,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
	}
}

func classifyResponsesError(statusCode, providerStatus int, message string, codes ...string) model.ErrorKind {
	return providererrors.Classify(statusCode, providerStatus, message, codes...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
