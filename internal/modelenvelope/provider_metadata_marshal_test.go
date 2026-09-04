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
	got, err := json.Marshal(unsafe)
	if err != nil || string(got) != `{}` {
		t.Fatalf("unsafe metadata serialized as %s (%v), want {}", got, err)
	}
	safe := ProviderMetadata{
		OpenRouter: OpenRouterMetadata{Provider: "Moonshot AI"},
		Anthropic:  AnthropicMetadata{CacheCreation: AnthropicCacheCreation{Ephemeral1hInputTokens: 7}},
	}
	want := `{"openrouter":{"provider":"Moonshot AI"},` +
		`"anthropic":{"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":7}}}`
	got, err = json.Marshal(safe)
	if err != nil || string(got) != want {
		t.Fatalf("safe metadata serialized as %s (%v), want %s", got, err, want)
	}
}

func TestAnthropicCacheCreationDecodesLeniently(t *testing.T) {
	var creation AnthropicCacheCreation
	malformed := []byte(`{"ephemeral_5m_input_tokens":"many","ephemeral_1h_input_tokens":3}`)
	if err := json.Unmarshal(malformed, &creation); err != nil || creation != (AnthropicCacheCreation{}) {
		t.Fatalf("malformed cache creation decoded as %+v (%v), want empty and no error", creation, err)
	}
	if err := json.Unmarshal([]byte(`{"ephemeral_1h_input_tokens":3}`), &creation); err != nil ||
		creation != (AnthropicCacheCreation{Ephemeral1hInputTokens: 3}) {
		t.Fatalf("cache creation decoded as %+v (%v)", creation, err)
	}
}
