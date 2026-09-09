package modelenvelope

import (
	"encoding/json"
	"strings"
)

type ProviderMetadata struct {
	OpenRouter OpenRouterMetadata `json:"openrouter,omitzero"`
}

type OpenRouterMetadata struct {
	Provider string `json:"provider,omitempty"`
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

const maxProviderNameBytes = 2_000

func (m ProviderMetadata) MarshalJSON() ([]byte, error) {
	if strings.ContainsRune(m.OpenRouter.Provider, 0) || len(m.OpenRouter.Provider) > maxProviderNameBytes {
		m.OpenRouter.Provider = ""
	}
	type plain ProviderMetadata
	return json.Marshal(plain(m))
}
