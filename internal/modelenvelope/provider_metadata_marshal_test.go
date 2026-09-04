package modelenvelope

import (
	"encoding/json"
	"testing"
)

func TestProviderMetadataSerializesWithoutUnsafeValues(t *testing.T) {
	got, err := json.Marshal(ProviderMetadata{OpenRouter: OpenRouterMetadata{Provider: "Moon\x00shot"}})
	if err != nil || string(got) != `{}` {
		t.Fatalf("unsafe metadata serialized as %s (%v), want {}", got, err)
	}
	got, err = json.Marshal(ProviderMetadata{OpenRouter: OpenRouterMetadata{Provider: "Moonshot AI"}})
	if err != nil || string(got) != `{"openrouter":{"provider":"Moonshot AI"}}` {
		t.Fatalf("safe metadata serialized as %s (%v)", got, err)
	}
}
