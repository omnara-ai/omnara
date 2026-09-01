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

func openRouterCacheControl(retention model.CacheRetention, providerModelSlug string) *chatCacheControl {
	if !model.OpenRouterSupportsCacheControl(providerModelSlug) {
		return nil
	}
	switch retention {
	case model.CacheRetentionShort:
		return &chatCacheControl{Type: "ephemeral"}
	case model.CacheRetentionLong:
		return &chatCacheControl{Type: "ephemeral", TTL: "1h"}
	default:
		return nil
	}
}

func markOpenRouterCacheBreakpoints(messages []chatMessage, control *chatCacheControl) []chatMessage {
	if control == nil || len(messages) == 0 {
		return messages
	}
	last := len(messages) - 1
	for last >= 0 && messages[last].Role == chatRoleAssistant {
		last--
	}
	if last >= 0 {
		messages[last] = markChatMessageCacheBreakpoint(messages[last], control)
	}
	if last != 0 && messages[0].Role == chatRoleSystem {
		messages[0] = markChatMessageCacheBreakpoint(messages[0], control)
	}
	return messages
}

func markChatMessageCacheBreakpoint(message chatMessage, control *chatCacheControl) chatMessage {
	if len(message.ProviderReplay) != 0 {
		return message
	}
	switch content := message.Content.(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return message
		}
		message.Content = []any{map[string]any{
			"type":          "text",
			"text":          content,
			"cache_control": control,
		}}
	case []any:
		if len(content) == 0 {
			return message
		}
		block, ok := content[len(content)-1].(map[string]any)
		if !ok {
			return message
		}
		marked := maps.Clone(block)
		marked["cache_control"] = control
		content[len(content)-1] = marked
	}
	return message
}
