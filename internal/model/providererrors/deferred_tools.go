package providererrors

import "strings"

const (
	DeferredToolsUnsupportedCode    = "deferred_tools_unsupported"
	DeferredToolsUnsupportedMessage = "Deferred tool loading is not supported on this model"
)

func deferredToolsUnsupported(message string) bool {
	return strings.Contains(normalizeMessage(message), "tool 'tool_search' is not supported with")
}

func userFacingMessage(message string) string {
	if deferredToolsUnsupported(message) {
		return DeferredToolsUnsupportedMessage
	}
	return message
}

func userFacingCode(code, message string) string {
	if deferredToolsUnsupported(message) {
		return DeferredToolsUnsupportedCode
	}
	return code
}
