package providererrors

import "strings"

const (
	DeferredToolsUnsupportedCode    = "deferred_tools_unsupported"
	DeferredToolsUnsupportedMessage = "Deferred tool loading is not supported on this model"
)

func DeferredToolsUnsupported(message string) bool {
	lower := normalizeMessage(message)
	if strings.Contains(lower, "tool_addition/tool_removal is not supported on this model") {
		return true
	}
	return strings.Contains(lower, "tool 'tool_search' is not supported with")
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
