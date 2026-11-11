package providererrors

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

// OpenRouterError carries uninterpreted extension fields from a wire envelope.
// Only the configured OpenRouter interpreter decodes them. An unrelated
// provider's metadata must not prevent its native error from being decoded.
type OpenRouterError struct {
	metadata  json.RawMessage
	errorType json.RawMessage
}

// OpenRouterMetadata selects Chat Completions' error.metadata extension.
func OpenRouterMetadata(raw json.RawMessage) OpenRouterError {
	return OpenRouterError{metadata: raw}
}

// OpenRouterType selects the canonical error_type extension used by Messages
// and Responses. Its position in the envelope is the adapter's responsibility.
func OpenRouterType(raw json.RawMessage) OpenRouterError {
	return OpenRouterError{errorType: raw}
}

// Present checks JSON presence without interpreting gateway fields. Null,
// empty objects/arrays and empty strings are not evidence of an error.
func (e OpenRouterError) Present() bool {
	return extensionPresent(e.metadata) || extensionPresent(e.errorType)
}

func extensionPresent(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return true
	}
	switch compact.String() {
	case "null", "{}", "[]", `""`:
		return false
	default:
		return true
	}
}

// https://openrouter.ai/docs/api/reference/errors-and-debugging
type openRouterError struct {
	ErrorType    string          `json:"error_type"`
	ProviderCode string          `json:"provider_code"`
	LimitSource  string          `json:"limit_source"`
	Reason       string          `json:"reason"`
	Raw          json.RawMessage `json:"raw"`
}

func (e OpenRouterError) decode() openRouterError {
	var decoded openRouterError
	if len(e.metadata) > 0 {
		if err := json.Unmarshal(e.metadata, &decoded); err != nil {
			return openRouterError{}
		}
	} else if len(e.errorType) > 0 {
		if err := json.Unmarshal(e.errorType, &decoded.ErrorType); err != nil {
			return openRouterError{}
		}
	}
	return decoded
}

const maxNestedProviderErrorBytes = 64 * 1024

func interpretOpenRouter(e Evidence, gateway openRouterError, code, message string) (string, string, []classification) {
	canonical := classifyOpenRouterType(gateway.ErrorType)
	upstream := classifyUpstreamCode(gateway.ProviderCode)
	// Providers can report a spending cap as a 429 rate-limit category. An
	// explicit upstream billing reason is more precise than that category.
	// Other conflicting canonical types retain their normal authority.
	if canonical.kind == model.ErrorKindRateLimit && upstream.kind == model.ErrorKindBillingAccount {
		canonical = upstream
	}
	signals := []classification{
		classifyOpenRouterLimit(gateway.LimitSource),
		e.Native.detail.classification, // A detailed reason refines a coarse rate-limit category.
		canonical,
		upstream,
		e.Native.codeField.classification,
		e.Native.typeField.classification,
	}
	code = firstNonEmpty(e.Native.detail.value, gateway.ErrorType, gateway.ProviderCode, code)
	if openRouterRawCanRefine(e, gateway, message) {
		if raw, ok := openRouterRawError(gateway.Raw); ok {
			message = firstNonEmpty(raw.Message, message)
			code = firstNonEmpty(raw.DetailCode, raw.Code, raw.Type, code)
			signals = append(signals,
				classifyUpstreamCode(raw.DetailCode), classifyUpstreamCode(raw.Code), classifyUpstreamCode(raw.Type))
		}
	}
	return code, message, signals
}

func classifyOpenRouterLimit(value string) classification {
	// This namespace describes OpenRouter's budgets, not native provider codes.
	// https://openrouter.ai/docs/api/reference/limits
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openrouter_in_flight_budget":
		return classification{kind: model.ErrorKindTransient}
	case "openrouter_credits", "openrouter_key_limit", "openrouter_workspace_budget", "openrouter_guardrail_budget":
		return classification{kind: model.ErrorKindBillingAccount}
	default:
		return classification{}
	}
}

func classifyOpenRouterType(value string) classification {
	// The canonical gateway type survives lossy translations into native API
	// codes. It has priority over those codes, but generic wrappers can refine.
	// invalid_request_error and provider_error are legacy aliases retained from
	// existing OpenRouter wire fixtures; the other values are documented types.
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "context_length_exceeded":
		return classification{kind: model.ErrorKindContextWindow}
	case "payload_too_large":
		return classification{kind: model.ErrorKindPayloadTooLarge}
	case "authentication":
		return classification{kind: model.ErrorKindAuth}
	case "permission_denied":
		return classification{kind: model.ErrorKindAuth, generic: true}
	case "payment_required":
		return classification{kind: model.ErrorKindBillingAccount}
	case "rate_limit_exceeded":
		return classification{kind: model.ErrorKindRateLimit}
	case "timeout":
		return classification{kind: model.ErrorKindTransient}
	case "max_tokens_exceeded", "token_limit_exceeded", "string_too_long", "not_found",
		"precondition_failed", "unprocessable", "content_policy_violation", "refusal",
		"invalid_image", "image_too_large", "image_too_small", "unsupported_image_format",
		"image_not_found", "image_download_failed":
		return classification{kind: model.ErrorKindInvalidRequest}
	case "server", "provider_overloaded", "provider_unavailable", "unmapped", "provider_error":
		return classification{kind: model.ErrorKindProviderUnavailable, generic: true}
	case "invalid_request", "invalid_prompt", "invalid_request_error":
		return classification{kind: model.ErrorKindInvalidRequest, generic: true}
	default:
		return classification{}
	}
}

func hasOpenRouterBillingMarker(message string) bool {
	message = normalizeMessage(message)
	return strings.Contains(message, "key limit exceeded") || strings.Contains(message, "budget limit exceeded")
}

var openRouterAffordableOutputTokensPattern = regexp.MustCompile(`can only afford (\d+)`)

// OpenRouter documents reducing max_tokens for credit refusals, but does not
// document a structured affordable-token count. This sentence is a best-effort
// hint only. Execution owns whether the returned allowance is usable.
func openRouterAffordableOutputTokens(message string) *int {
	match := openRouterAffordableOutputTokensPattern.FindStringSubmatch(message)
	if match == nil {
		return nil
	}
	tokens, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	return &tokens
}

func openRouterRawCanRefine(e Evidence, gateway openRouterError, message string) bool {
	// Masked 5xx failures and specific outer failures must not be replaced by
	// raw upstream details. Only a generic 400 wrapper can be refined.
	if effectiveStatusCode(e.HTTPStatus, e.InBandStatus) != http.StatusBadRequest ||
		!genericProviderErrorMessage(message) || strings.TrimSpace(gateway.LimitSource) != "" {
		return false
	}
	if !refinableCode(gateway.ErrorType, classifyOpenRouterType(gateway.ErrorType), false) ||
		!refinableCode(gateway.ProviderCode, classifyUpstreamCode(gateway.ProviderCode), true) {
		return false
	}
	for _, field := range []nativeCode{e.Native.detail, e.Native.codeField, e.Native.typeField} {
		if !refinableCode(field.value, field.classification, false) {
			return false
		}
	}
	return true
}

func refinableCode(value string, signal classification, allowUnknown bool) bool {
	if strings.TrimSpace(value) == "" || StatusCode(value) != 0 {
		return true
	}
	if signal.kind == "" {
		return allowUnknown
	}
	return signal.generic && deterministicKindCanBeOverridden(signal.kind)
}

func genericProviderErrorMessage(message string) bool {
	message = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(message)), ".")
	return message == "" || message == "provider returned error" || message == "provider returned an error"
}

type upstreamError struct {
	Code       string
	Type       string
	DetailCode string
	Message    string
}

func openRouterRawError(raw json.RawMessage) (upstreamError, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxNestedProviderErrorBytes || bytes.Equal(raw, []byte("null")) {
		return upstreamError{}, false
	}
	message := openRouterRawErrorMessage(raw)
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return upstreamError{}, false
		}
		raw = []byte(encoded)
		if len(raw) == 0 || len(raw) > maxNestedProviderErrorBytes {
			return upstreamError{}, false
		}
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Error) > 0 && envelope.Error[0] == '{' {
		raw = envelope.Error
	}
	var decoded struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
		Details struct {
			ErrorCode string `json:"error_code"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return upstreamError{Message: message}, message != ""
	}
	result := upstreamError{
		Code: CodeText(decoded.Code), Type: decoded.Type, DetailCode: decoded.Details.ErrorCode,
		Message: firstNonEmpty(decoded.Message, message),
	}
	return result, result.Code != "" || result.Type != "" || result.DetailCode != "" || result.Message != ""
}

func openRouterRawErrorMessage(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	fields := [...]string{"message", "error", "detail", "details", "msg"}
	stack := []any{value}
	for len(stack) > 0 {
		value = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch value := value.(type) {
		case string:
			message := strings.TrimSpace(value)
			if message == "" {
				continue
			}
			var nested any
			if err := json.Unmarshal([]byte(message), &nested); err == nil {
				if _, ok := nested.(map[string]any); ok {
					stack = append(stack, nested)
					continue
				}
			}
			return message
		case map[string]any:
			for index := len(fields) - 1; index >= 0; index-- {
				if nested, ok := value[fields[index]]; ok {
					stack = append(stack, nested)
				}
			}
		}
	}
	return ""
}

// Keep only scalar diagnostics. Raw upstream bodies can contain prompts or
// masked provider details and must never enter the persisted metadata channel.
func openRouterDiagnostics(e Evidence, gateway openRouterError) json.RawMessage {
	fields := map[string]string{}
	for key, value := range map[string]string{
		"native_code": e.Native.codeField.value, "native_type": e.Native.typeField.value, "native_detail_code": e.Native.detail.value,
		"error_type": gateway.ErrorType, "provider_code": gateway.ProviderCode,
		"limit_source": gateway.LimitSource, "reason": gateway.Reason,
	} {
		if value != "" {
			fields[key] = value
		}
	}
	if len(fields) == 0 {
		return nil
	}
	// The downstream retry-evidence validator bounds metadata before persistence.
	encoded, err := json.Marshal(map[string]map[string]string{"openrouter": fields})
	if err != nil {
		return nil
	}
	return encoded
}
