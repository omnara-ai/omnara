package openaichatcompletions

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
	return string(modelprotocol.APIFormatOpenAIChatCompletions)
}

func (p protocol) invalidResponseError(
	resp route.Response,
	response chatCompletionsResponse,
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

func classifyHTTPError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	body []byte,
) model.ProviderError {
	message := strings.TrimSpace(string(body))
	var envelope chatProviderErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		return classifyProviderError(
			source,
			variant,
			statusCode,
			header,
			envelope.Error,
			message,
		)
	}
	return classifyProviderError(source, variant, statusCode, header, chatProviderError{}, message)
}

func classifyChoiceError(
	source string,
	variant modelprotocol.APIVariant,
	httpStatusCode int,
	header http.Header,
	choice chatChoice,
) model.ProviderError {
	providerErr := choice.Error
	if !providerErr.present() {
		providerErr = chatProviderError{
			Message: "provider returned finish_reason error",
			Type:    "finish_reason_error",
			Code:    "finish_reason_error",
		}
	}
	return classifyProviderError(source, variant, httpStatusCode, header, providerErr, "")
}

func classifyProviderError(
	source string,
	variant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	providerErr chatProviderError,
	fallbackMessage string,
) model.ProviderError {
	message := strings.TrimSpace(providerErr.Message)
	if message == "" {
		message = fallbackMessage
	}
	result := providererrors.Interpret(variant, providererrors.Evidence{
		HTTPStatus:   statusCode,
		InBandStatus: providererrors.StatusCode(providerErr.Code),
		Message:      message,
		Native:       providererrors.OpenAI(providererrors.CodeText(providerErr.Code), providerErr.Type),
		OpenRouter:   providererrors.OpenRouterMetadata(providerErr.Metadata),
	})
	result.Source = source
	result.RequestID = model.RequestIDFromHeader(header)
	result.RetryAfter = model.RetryAfterFromHeader(header)
	return result
}

type chatProviderErrorEnvelope struct {
	Error chatProviderError `json:"error"`
}

type chatProviderError struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Code     any             `json:"code"`
	Metadata json.RawMessage `json:"metadata"`
}

func (e chatProviderError) present() bool {
	return e.Message != "" ||
		e.Type != "" ||
		providererrors.CodeText(e.Code) != "" ||
		providererrors.OpenRouterMetadata(e.Metadata).Present()
}
