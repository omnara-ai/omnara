package modelenvelope

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestProviderMetadataJSONShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata ProviderMetadata
		want     string
	}{
		{name: "empty", want: `{}`},
		{
			name:     "openrouter",
			metadata: ProviderMetadata{OpenRouter: OpenRouterMetadata{Provider: "Moonshot AI"}},
			want:     `{"openrouter":{"provider":"Moonshot AI"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.metadata)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != tc.want {
				t.Fatalf("encoded = %s, want %s", encoded, tc.want)
			}
			var decoded ProviderMetadata
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if reencoded, _ := json.Marshal(decoded); string(reencoded) != tc.want {
				t.Fatalf("round trip = %s, want %s", reencoded, tc.want)
			}
		})
	}
	encoded, err := json.Marshal(ResponseEnvelope{})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if bytes.Contains(encoded, []byte("provider_metadata")) {
		t.Fatalf("empty provider metadata must be omitted from the envelope: %s", encoded)
	}
}
