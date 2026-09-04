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

func TestProviderMetadataDecodesLeniently(t *testing.T) {
	for _, raw := range []string{`{"openrouter":"not an object"}`, `{"openrouter":{"provider":7}}`, `[]`} {
		var decoded ProviderMetadata
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded != (ProviderMetadata{}) {
			t.Fatalf("%s decoded as %+v (%v), want empty and no error", raw, decoded, err)
		}
	}
	var decoded ProviderMetadata
	if err := json.Unmarshal([]byte(`{"openrouter":{"provider":"Moonshot AI"},"future":{"x":1}}`), &decoded); err != nil ||
		decoded.OpenRouter.Provider != "Moonshot AI" {
		t.Fatalf("decoded = %+v (%v), want provider kept and unknown family ignored", decoded, err)
	}
}
