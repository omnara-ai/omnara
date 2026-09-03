package openaichatcompletions

import (
	"maps"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

type chatCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func applyPromptCachePlan(payload *chatCompletionsRequest, plan model.PromptCachePlan) {
	switch plan.Affinity {
	case model.PromptCacheAffinitySessionID:
		payload.SessionID = plan.ConversationKey
	case model.PromptCacheAffinityPromptCacheKey:
		payload.PromptCacheKey = plan.ConversationKey
	case model.PromptCacheAffinityNone:
	}
	if !plan.Explicit {
		return
	}
	control := &chatCacheControl{Type: "ephemeral"}
	if plan.LongRetention {
		control.TTL = "1h"
	}
	payload.Messages = markCacheBreakpoints(payload.Messages, control)
}

func markCacheBreakpoints(messages []chatMessage, control *chatCacheControl) []chatMessage {
	if len(messages) == 0 {
		return messages
	}
	tail := len(messages) - 1
	for tail > 0 && (messages[tail].Role == chatRoleAssistant ||
		messages[tail].Role == chatRoleSystem ||
		!acceptsCacheBreakpoint(messages[tail])) {
		tail--
	}
	if acceptsCacheBreakpoint(messages[tail]) {
		messages[tail] = withCacheBreakpoint(messages[tail], control)
	}
	if tail != 0 && messages[0].Role == chatRoleSystem && acceptsCacheBreakpoint(messages[0]) {
		messages[0] = withCacheBreakpoint(messages[0], control)
	}
	return messages
}

func acceptsCacheBreakpoint(message chatMessage) bool {
	if len(message.ProviderReplay) != 0 {
		return false
	}
	switch content := message.Content.(type) {
	case string:
		return strings.TrimSpace(content) != ""
	case []any:
		if len(content) == 0 {
			return false
		}
		_, ok := content[len(content)-1].(map[string]any)
		return ok
	}
	return false
}

func withCacheBreakpoint(message chatMessage, control *chatCacheControl) chatMessage {
	switch content := message.Content.(type) {
	case string:
		message.Content = []any{map[string]any{
			"type":          "text",
			"text":          content,
			"cache_control": control,
		}}
	case []any:
		if block, ok := content[len(content)-1].(map[string]any); ok {
			marked := maps.Clone(block)
			marked["cache_control"] = control
			content[len(content)-1] = marked
		}
	}
	return message
}
