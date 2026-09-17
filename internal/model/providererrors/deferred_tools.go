package providererrors

import "strings"

const (
	DeferredToolsUnsupportedCode    = "deferred_tools_unsupported"
	DeferredToolsUnsupportedMessage = "Deferred tool loading is not supported on this model"
)

func DeferredToolsUnsupported(message string) bool {
	return strings.Contains(normalizeMessage(message), "tool 'tool_search' is not supported with")
}

func UserFacingMessage(message string) string {
	if DeferredToolsUnsupported(message) {
		return DeferredToolsUnsupportedMessage
	}
	return message
}

func UserFacingCode(code, message string) string {
	if DeferredToolsUnsupported(message) {
		return DeferredToolsUnsupportedCode
	}
	return code
}
