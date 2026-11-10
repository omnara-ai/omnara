package modelenvelope

import (
	"encoding/json"
	"strings"
)

type ProviderMetadata struct {
	OpenRouter OpenRouterMetadata `json:"openrouter,omitzero"`
	Anthropic  AnthropicMetadata  `json:"anthropic,omitzero"`
	OpenAI     OpenAIMetadata     `json:"openai,omitzero"`
}

type OpenRouterMetadata struct {
	Provider string `json:"provider,omitempty"`
}

type AnthropicMetadata struct {
	Speed                   string `json:"speed,omitempty"`
	InferenceGeo            string `json:"inference_geo,omitempty"`
	ServiceTier             string `json:"service_tier,omitempty"`
	CacheWrite1hInputTokens *int   `json:"cache_write_1h_input_tokens,omitempty"`
}

type OpenAIMetadata struct {
	ServiceTier              string `json:"service_tier,omitempty"`
	CacheWrite12hInputTokens int    `json:"cache_write_12h_input_tokens,omitempty"`
}

func (m *ProviderMetadata) UnmarshalJSON(data []byte) error {
	type plain ProviderMetadata
	var decoded plain
	if json.Unmarshal(data, &decoded) != nil {
		decoded = plain{}
	}
	*m = ProviderMetadata(decoded)
	return nil
}

const (
	maxProviderNameBytes = 2_000
	maxRateClassBytes    = 64
)

func (m ProviderMetadata) MarshalJSON() ([]byte, error) {
	m.OpenRouter.Provider = boundedMetadataText(m.OpenRouter.Provider, maxProviderNameBytes)
	m.Anthropic.Speed = boundedMetadataText(m.Anthropic.Speed, maxRateClassBytes)
	m.Anthropic.InferenceGeo = boundedMetadataText(m.Anthropic.InferenceGeo, maxRateClassBytes)
	m.Anthropic.ServiceTier = boundedMetadataText(m.Anthropic.ServiceTier, maxRateClassBytes)
	if m.Anthropic.CacheWrite1hInputTokens != nil && *m.Anthropic.CacheWrite1hInputTokens < 0 {
		m.Anthropic.CacheWrite1hInputTokens = nil
	}
	m.OpenAI.ServiceTier = boundedMetadataText(m.OpenAI.ServiceTier, maxRateClassBytes)
	m.OpenAI.CacheWrite12hInputTokens = max(m.OpenAI.CacheWrite12hInputTokens, 0)
	type plain ProviderMetadata
	return json.Marshal(plain(m))
}

func boundedMetadataText(value string, limit int) string {
	if strings.ContainsRune(value, 0) || len(value) > limit {
		return ""
	}
	return value
}

func OpenAIRateClass(serviceTier string, cacheWrite12hTokens int) OpenAIMetadata {
	out := OpenAIMetadata{CacheWrite12hInputTokens: max(cacheWrite12hTokens, 0)}
	if serviceTier != "default" {
		out.ServiceTier = serviceTier
	}
	return out
}
