package providererrors

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

// Evidence keeps protocol-native fields separate from gateway extensions.
// Adapters decode envelopes and headers; this package interprets their meaning.
type Evidence struct {
	HTTPStatus   int
	InBandStatus int // Optional status decoded by the adapter; numeric-code semantics differ by API format.
	Message      string
	Native       NativeError
	OpenRouter   OpenRouterError
}

// NativeError can only be constructed through the format-specific helpers.
// This keeps the vocabulary decision at the adapter boundary without adding a
// second format or provider identity to the interpreted evidence.
type NativeError struct {
	detail    nativeCode
	codeField nativeCode
	typeField nativeCode
}

type nativeCode struct {
	value          string
	classification classification
}

// OpenAI interprets native Chat Completions or Responses code/type fields.
func OpenAI(code, errorType string) NativeError {
	return NativeError{
		codeField: nativeCode{value: code, classification: classifyOpenAICode(code)},
		typeField: nativeCode{value: errorType, classification: classifyOpenAICode(errorType)},
	}
}

// Anthropic interprets Messages type and details.error_code fields. A detailed
// spend-limit code takes precedence over the coarse rate_limit_error type.
func Anthropic(errorType, detailCode string) NativeError {
	return NativeError{
		detail:    nativeCode{value: detailCode, classification: classifyAnthropicCode(detailCode)},
		typeField: nativeCode{value: errorType, classification: classifyAnthropicCode(errorType)},
	}
}

// Interpret returns normalized facts, never a decision to retry. The configured
// variant selects extensions, including when the endpoint is behind a proxy.
// Source, request ID and retry headers remain the protocol adapter's responsibility.
func Interpret(variant modelprotocol.APIVariant, evidence Evidence) model.ProviderError {
	message := strings.TrimSpace(evidence.Message)
	code := evidence.Native.code()
	signals := evidence.Native.signals()
	openRouter := variant == modelprotocol.APIVariantOpenRouter
	var gateway openRouterError
	if openRouter {
		gateway = evidence.OpenRouter.decode()
		code, message, signals = interpretOpenRouter(evidence, gateway, code, message)
	}
	status := effectiveStatusCode(evidence.HTTPStatus, evidence.InBandStatus)
	kind := classify(status, message, signals, openRouter && hasOpenRouterBillingMarker(message))
	result := model.ProviderError{
		Kind: kind, StatusCode: status,
		Code: userFacingCode(code, message), Message: userFacingMessage(message),
	}
	if openRouter {
		if kind == model.ErrorKindBillingAccount {
			result.AffordableOutputTokens = openRouterAffordableOutputTokens(message)
		}
		result.Metadata = openRouterDiagnostics(evidence, gateway)
	}
	return result
}

func (e NativeError) code() string {
	return firstNonEmpty(e.detail.value, e.codeField.value, e.typeField.value)
}

func (e NativeError) signals() []classification {
	return []classification{e.detail.classification, e.codeField.classification, e.typeField.classification}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
