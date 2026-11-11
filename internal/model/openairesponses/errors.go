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
	variant modelprotocol.APIVariant,
	response responsesResponse,
	statusCode int,
	header http.Header,
) model.ProviderError {
	message := response.Error.Message
	if message == "" {
		message = "openai-responses request failed"
	}
	return interpretResponsesError(source, variant, statusCode, header, response.Error, response.ErrorType, message)
}

func classifyHTTPError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	body []byte,
) model.ProviderError {
	message := strings.TrimSpace(string(body))
	var envelope struct {
		ErrorType json.RawMessage `json:"error_type"`
		Error     responsesError  `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return interpretResponsesError(source, variant, statusCode, header, responsesError{}, nil, message)
	}
	return interpretResponsesError(source, variant, statusCode, header, envelope.Error, envelope.ErrorType, message)
}

func interpretResponsesError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	native responsesError,
	openRouterType json.RawMessage,
	fallbackMessage string,
) model.ProviderError {
	result := providererrors.Interpret(variant, providererrors.Evidence{
		HTTPStatus: statusCode, Message: firstNonEmpty(native.Message, fallbackMessage),
		Native:     providererrors.OpenAI(native.codeText(), native.Type),
		OpenRouter: providererrors.OpenRouterType(openRouterType),
	})
	result.Source = source
	result.RequestID = model.RequestIDFromHeader(header)
	result.RetryAfter = model.RetryAfterFromHeader(header)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
