package openaichatcompletions

import (
	"maps"

	"github.com/omnara-ai/omnara/internal/model"
)

type chatCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func applyPromptCachePlan(payload *chatCompletionsRequest, plan model.PromptCachePlan, compat compat) {
	if plan.ConversationKey != "" {
		switch compat.conversationKeyField {
		case conversationKeyFieldSessionID:
			payload.SessionID = plan.ConversationKey
		case conversationKeyFieldPromptCacheKey:
			payload.PromptCacheKey = plan.ConversationKey
		}
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
	for i, message := range messages {
		if text, ok := message.Content.(string); ok && message.Role != chatRoleAssistant && text != "" {
			messages[i].Content = []any{map[string]any{"type": "text", "text": text}}
		}
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
	content, ok := message.Content.([]any)
	if !ok || len(content) == 0 {
		return false
	}
	_, ok = content[len(content)-1].(map[string]any)
	return ok
}

func withCacheBreakpoint(message chatMessage, control *chatCacheControl) chatMessage {
	content, _ := message.Content.([]any)
	if block, ok := content[len(content)-1].(map[string]any); ok {
		marked := maps.Clone(block)
		marked["cache_control"] = control
		content[len(content)-1] = marked
	}
	return message
}
