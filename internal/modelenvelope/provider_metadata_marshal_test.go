package modelenvelope

import (
	"encoding/json"
	"testing"
)

func TestProviderMetadataSerializesWithoutUnsafeValues(t *testing.T) {
	unsafe := ProviderMetadata{
		OpenRouter: OpenRouterMetadata{Provider: "Moon\x00shot"},
		Anthropic:  AnthropicMetadata{CacheCreation: AnthropicCacheCreation{Ephemeral5mInputTokens: -1}},
	}
	if got, _ := json.Marshal(unsafe); string(got) != `{}` {
		t.Fatalf("unsafe metadata serialized as %s, want {}", got)
	}
	safe := ProviderMetadata{
		OpenRouter: OpenRouterMetadata{Provider: "Moonshot AI"},
		Anthropic:  AnthropicMetadata{CacheCreation: AnthropicCacheCreation{Ephemeral1hInputTokens: 7}},
	}
	want := `{"openrouter":{"provider":"Moonshot AI"},` +
		`"anthropic":{"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":7}}}`
	if got, _ := json.Marshal(safe); string(got) != want {
		t.Fatalf("safe metadata serialized as %s, want %s", got, want)
	}
}
