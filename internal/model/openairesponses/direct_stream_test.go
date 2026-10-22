package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestDirectStreamMatchesLegacyConstruction(t *testing.T) {
	for _, variant := range []modelprotocol.APIVariant{
		modelprotocol.APIVariantDefault, modelprotocol.APIVariantOpenRouter,
	} {
		for _, options := range []string{
			"", "null", `{}`,
			`{"stream":false,"model":"ignored","messages":[],"input":[],"max_tokens":12,"max_output_tokens":13,"max_completion_tokens":14}`,
			`{"stream_options":{"include_usage":false,"extra":{"z":1,"a":2}},"temperature":1e-1}`,
			`{"stream_options":null,"metadata":{"b":9007199254740993,"a":[1,1.0,1e0,1.25e2,-0]}}`,
			`{"metadata":{"z":"雪 <>& \u2028","a":{"z":2,"a":1}},"session_id":"session"}`,
			`{"metadata":{"a":{"a":1,"z":2},"z":"雪 <>& \u2028"},"session_id":"session"}`,
			`[]`, `{"broken":`,
		} {
			t.Run(string(variant)+"/"+options, func(t *testing.T) {
				p := protocol{client: Client{ProviderModelSlug: "test-model",
					EndpointPath:      testEndpointPath,
					APIVariant:        variant,
					APIVariantOptions: json.RawMessage(options)}}
				input := model.PrepareInput{
					Context: modelcontext.Bundle{SystemPrompt: "System <>& 雪",
						Messages: []modelcontext.Message{{Sequence: 1,
							Role:    modelprotocol.RoleUser,
							Content: json.RawMessage(`[{"type":"text","text":"hello <>& 雪"}]`)}},
						ToolSpecs: []modelcontext.ToolSpec{{Name: "lookup",
							InputSchema: json.RawMessage(`{"type":"object","properties":{"z":{"type":"number"},"a":{"type":"string"}}}`)}}},
					Policy: model.RequestPolicy{MaxOutputTokens: 1024},
				}
				base, oldErr := p.BuildRequest(t.Context(), input)
				var legacy json.RawMessage
				if oldErr == nil {
					legacy, oldErr = p.BuildStreamRequest(base)
				}
				direct, err := p.BuildStreamingRequest(t.Context(), input)
				if oldErr != nil {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.JSONEq(t, string(legacy), string(direct))
				require.Equal(t,
					model.RequestInputFingerprint(legacy, "https://example.test/endpoint"),
					model.RequestInputFingerprint(direct, "https://example.test/endpoint"))
			})
		}
	}
}
