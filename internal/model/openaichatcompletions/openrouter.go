package openaichatcompletions

import (
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
)

type chatCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func openRouterCacheControl(retention model.CacheRetention, providerModelSlug string) *chatCacheControl {
	if !openRouterSupportsTopLevelCacheControl(providerModelSlug) {
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

func openRouterSupportsTopLevelCacheControl(providerModelSlug string) bool {
	slug := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(providerModelSlug)), "~")
	return strings.HasPrefix(slug, "anthropic/claude-")
}
