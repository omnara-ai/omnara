package modelenvelope

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
