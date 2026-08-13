package modelprotocol

type APIFormat string

type APIVariant string

type MessageRole string

type ErrorKind string

const (
	APIFormatOpenAIResponses       APIFormat = "openai-responses"
	APIFormatOpenAIChatCompletions APIFormat = "openai-chat-completions"
	APIFormatAnthropicMessages     APIFormat = "anthropic-messages"

	APIVariantDefault    APIVariant = "default"
	APIVariantOpenRouter APIVariant = "openrouter"
	APIVariantBedrock    APIVariant = "bedrock"

	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"

	ErrorKindContextWindow       ErrorKind = "context_window"
	ErrorKindPayloadTooLarge     ErrorKind = "payload_too_large"
	ErrorKindRateLimit           ErrorKind = "rate_limit"
	ErrorKindTransient           ErrorKind = "transient"
	ErrorKindAuth                ErrorKind = "auth"
	ErrorKindBillingAccount      ErrorKind = "billing_account"
	ErrorKindInvalidRequest      ErrorKind = "invalid_request"
	ErrorKindProviderUnavailable ErrorKind = "provider_unavailable"
	ErrorKindReplayRejected      ErrorKind = "replay_rejected"
	ErrorKindRuntime             ErrorKind = "runtime"
	ErrorKindCanceled            ErrorKind = "canceled"
	ErrorKindUnknown             ErrorKind = "unknown"
)
