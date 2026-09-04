package modelenvelope

import (
	"encoding/json"
	"strings"
)

type ProviderMetadata struct {
	OpenRouter OpenRouterMetadata `json:"openrouter,omitzero"`
	Anthropic  AnthropicMetadata  `json:"anthropic,omitzero"`
}

type OpenRouterMetadata struct {
	Provider string `json:"provider,omitempty"`
}

type AnthropicMetadata struct {
	CacheCreation AnthropicCacheCreation `json:"cache_creation"`
}

type AnthropicCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

const maxProviderNameBytes = 2_000

func (m ProviderMetadata) MarshalJSON() ([]byte, error) {
	if strings.ContainsRune(m.OpenRouter.Provider, 0) || len(m.OpenRouter.Provider) > maxProviderNameBytes {
		m.OpenRouter.Provider = ""
	}
	if creation := m.Anthropic.CacheCreation; creation.Ephemeral5mInputTokens < 0 ||
		creation.Ephemeral1hInputTokens < 0 {
		m.Anthropic.CacheCreation = AnthropicCacheCreation{}
	}
	type plain ProviderMetadata
	return json.Marshal(plain(m))
}
