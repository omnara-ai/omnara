package modelenvelope

import (
	"encoding/json"
	"strings"
)

type ProviderMetadata struct {
	RequestMaxOutputTokens int                `json:"request_max_output_tokens,omitempty"`
	OpenRouter             OpenRouterMetadata `json:"openrouter,omitzero"`
}

type OpenRouterMetadata struct {
	Provider           string `json:"provider,omitempty"`
	FinishReason       string `json:"finish_reason,omitempty"`
	NativeFinishReason string `json:"native_finish_reason,omitempty"`
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
	for _, reason := range []*string{&m.OpenRouter.FinishReason, &m.OpenRouter.NativeFinishReason} {
		if strings.ContainsRune(*reason, 0) || len(*reason) > 256 {
			*reason = ""
		}
	}
	type plain ProviderMetadata
	return json.Marshal(plain(m))
}
