package providererrors

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

// Relayed provider codes retain their original vocabulary even when the outer
// envelope uses another API format. Do not interpret gateway metadata here.
func classifyUpstreamCode(value string) classification {
	openAI, anthropic := classifyOpenAICode(value), classifyAnthropicCode(value)
	if openAI.kind == "" {
		return anthropic
	}
	if anthropic.kind == "" {
		return openAI
	}
	// A relayed code does not identify its originating API. If the vocabularies
	// ever disagree, fall back to other evidence instead of guessing its owner.
	if openAI.kind != anthropic.kind {
		return classification{}
	}
	return classification{kind: openAI.kind, generic: openAI.generic || anthropic.generic}
}

// These aliases occur on compatible servers but are not part of the documented
// native OpenAI or Anthropic tables. Keep them explicit instead of guessing from
// arbitrary strings or treating them as OpenRouter's extension vocabulary.
func classifyCompatibleCode(value string) classification {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "context_window_exceeded", "model_context_window_exceeded":
		return classification{kind: model.ErrorKindContextWindow}
	case "payload_too_large":
		return classification{kind: model.ErrorKindPayloadTooLarge}
	case "permission_denied":
		return classification{kind: model.ErrorKindAuth, generic: true}
	case "network_error":
		return classification{kind: model.ErrorKindTransient}
	case "moderation", "content_policy", "safety_policy":
		return classification{kind: model.ErrorKindInvalidRequest}
	case "provider_error":
		return classification{kind: model.ErrorKindProviderUnavailable, generic: true}
	default:
		return classification{}
	}
}

// Exact vendor messages can be forwarded by gateways (including LiteLLM) in
// any outer format. These conservative recognizers therefore belong with
// upstream evidence, not with the gateway that happens to carry them.
func hasPayloadTooLargeMarker(message string) bool {
	return strings.Contains(message, "request entity too large") ||
		strings.Contains(message, "payload too large") ||
		strings.Contains(message, "request body too large") ||
		strings.Contains(message, "request too large") ||
		strings.Contains(message, "request_too_large")
}

func normalizeMessage(message string) string {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.ReplaceAll(message, "`", "")
}

func hasReplayRejectionMarker(message string) bool {
	encryptedContent := strings.Contains(message, "encrypted content") ||
		strings.Contains(message, "encrypted function output content")
	if encryptedContent &&
		(strings.Contains(message, "could not be verified") ||
			strings.Contains(message, "could not be decrypted") ||
			strings.Contains(message, "could not be decoded")) {
		return true
	}
	if strings.Contains(message, "invalid signature in thinking") {
		return true
	}
	// https://platform.claude.com/docs/en/api/errors#thinking-blocks-cannot-be-modified
	return strings.Contains(message, "thinking or redacted_thinking blocks") &&
		strings.Contains(message, "latest assistant message") &&
		(strings.Contains(message, "cannot be modified") ||
			strings.Contains(message, "must remain as they were"))
}

func hasContextWindowMarker(message string) bool {
	return strings.Contains(message, "maximum context length") ||
		strings.Contains(message, "reduce the length of the messages") ||
		strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "exceeds the context window") ||
		strings.Contains(message, "input length and max_tokens exceed context limit")
}

func hasBillingMarker(message string) bool {
	return strings.Contains(message, "insufficient_quota") ||
		strings.Contains(message, "insufficient quota") ||
		strings.Contains(message, "billing_hard_limit") ||
		strings.Contains(message, "billing hard limit") ||
		strings.Contains(message, "please check your plan and billing details") ||
		strings.Contains(message, "credit balance is too low") ||
		strings.Contains(message, "api usage limits")
}

func hasModerationMarker(message string) bool {
	return strings.Contains(message, "moderation") ||
		strings.Contains(message, "content_filter") ||
		strings.Contains(message, "content filter") ||
		strings.Contains(message, "content policy") ||
		strings.Contains(message, "safety policy")
}
