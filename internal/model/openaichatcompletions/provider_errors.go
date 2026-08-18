package openaichatcompletions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const maxNestedProviderErrorBytes = 64 * 1024

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
	apiVariant modelprotocol.APIVariant,
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
			apiVariant,
			statusCode,
			header,
			envelope.Error,
			message,
		)
	}
	return classifyProviderError(source, apiVariant, statusCode, header, chatProviderError{}, message)
}

func classifyChoiceError(
	source string,
	apiVariant modelprotocol.APIVariant,
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
	return classifyProviderError(source, apiVariant, httpStatusCode, header, providerErr, "")
}

func classifyProviderError(
	source string,
	apiVariant modelprotocol.APIVariant,
	statusCode int,
	header http.Header,
	providerErr chatProviderError,
	fallbackMessage string,
) model.ProviderError {
	message := strings.TrimSpace(providerErr.Message)
	if message == "" {
		message = fallbackMessage
	}
	code := providerErr.codeText()
	providerStatus := providererrors.StatusCode(providerErr.Code)
	effectiveStatus := providererrors.EffectiveStatusCode(statusCode, providerStatus)
	classificationMessage := message
	classificationValues := providerErr.classificationValues()
	refineFromRaw := apiVariant == modelprotocol.APIVariantOpenRouter &&
		genericProviderErrorMessage(message) &&
		rawErrorCanRefine(effectiveStatus, classificationValues)
	if refineFromRaw {
		if rawError, ok := providerErr.rawError(); ok {
			if rawMessage := strings.TrimSpace(rawError.Message); rawMessage != "" {
				classificationMessage = rawMessage
				message = rawMessage
			}
			classificationValues = append(classificationValues, rawError.classificationValues()...)
			if rawCode := rawError.codeText(); rawCode != "" {
				code = rawCode
			}
		}
	}
	kind := providererrors.Classify(
		statusCode,
		providerStatus,
		classificationMessage,
		classificationValues...,
	)
	return model.ProviderError{
		Kind:       kind,
		Source:     source,
		StatusCode: effectiveStatus,
		Code:       code,
		Message:    message,
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
	}
}

type chatProviderErrorEnvelope struct {
	Error chatProviderError `json:"error"`
}

type chatProviderError struct {
	Message  string                    `json:"message"`
	Type     string                    `json:"type"`
	Code     any                       `json:"code"`
	Metadata chatProviderErrorMetadata `json:"metadata"`
}

func (e chatProviderError) present() bool {
	raw := bytes.TrimSpace(e.Metadata.Raw)
	return e.Message != "" ||
		e.Type != "" ||
		providererrors.CodeText(e.Code) != "" ||
		e.Metadata.ErrorType != "" ||
		e.Metadata.ProviderCode != "" ||
		(len(raw) > 0 && !bytes.Equal(raw, []byte("null")))
}

func (e chatProviderError) rawError() (chatProviderError, bool) {
	raw := bytes.TrimSpace(e.Metadata.Raw)
	if len(raw) == 0 || len(raw) > maxNestedProviderErrorBytes || bytes.Equal(raw, []byte("null")) {
		return chatProviderError{}, false
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return chatProviderError{}, false
		}
		raw = []byte(encoded)
		if len(raw) == 0 || len(raw) > maxNestedProviderErrorBytes {
			return chatProviderError{}, false
		}
	}
	var envelope chatProviderErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.present() {
		return envelope.Error, true
	}
	var flat chatProviderError
	if err := json.Unmarshal(raw, &flat); err != nil || !flat.present() {
		return chatProviderError{}, false
	}
	return flat, true
}

func genericProviderErrorMessage(message string) bool {
	message = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(message)), ".")
	return message == "" || message == "provider returned error" ||
		message == "provider returned an error"
}

func rawErrorCanRefine(statusCode int, structuredValues []string) bool {
	// https://openrouter.ai/docs/api/reference/errors-and-debugging#masking-and-raw-provider-details
	return statusCode == http.StatusBadRequest &&
		providererrors.StructuredEvidenceCanBeRefined(structuredValues...)
}

func (e chatProviderError) codeText() string {
	if e.Metadata.ErrorType != "" {
		return e.Metadata.ErrorType
	}
	if e.Metadata.ProviderCode != "" {
		return e.Metadata.ProviderCode
	}
	if code := providererrors.CodeText(e.Code); code != "" {
		return code
	}
	return e.Type
}

func (e chatProviderError) classificationValues() []string {
	return []string{
		e.Metadata.ErrorType,
		e.Metadata.ProviderCode,
		providererrors.CodeText(e.Code),
		e.Type,
	}
}

type chatProviderErrorMetadata struct {
	ErrorType    string          `json:"error_type"`
	ProviderCode string          `json:"provider_code"`
	Raw          json.RawMessage `json:"raw"`
}
