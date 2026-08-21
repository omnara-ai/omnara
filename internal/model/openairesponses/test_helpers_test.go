package openairesponses

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const testModelProviderConfigID = "00000000-0000-4000-8000-000000000001"

func testProviderReplayIdentity(
	providerModelSlug string,
	apiFormat modelprotocol.APIFormat,
) modelenvelope.ProviderReplayIdentity {
	return modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      testModelProviderConfigID,
		RequestedProviderModelSlug: providerModelSlug,
		APIFormat:                  apiFormat,
		APIVariant:                 modelprotocol.APIVariantDefault,
	}
}

type providerReplayFixture struct {
	source  modelenvelope.ProviderReplayIdentity
	payload json.RawMessage
}

func testProviderReplay(
	providerModelSlug string,
	apiFormat modelprotocol.APIFormat,
	payload json.RawMessage,
) providerReplayFixture {
	return providerReplayFixture{
		source:  testProviderReplayIdentity(providerModelSlug, apiFormat),
		payload: payload,
	}
}

func providerReplayItemsForTest(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode provider replay: %v payload=%s", err, raw)
	}
	return items
}

func testRespondClient(server *httptest.Server) Client {
	return Client{
		ModelProviderConfigID: testModelProviderConfigID,
		Auth:                  route.BearerToken{Token: "test-key"},
		BaseURL:               server.URL,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
		HTTPClient:            server.Client(),
	}
}

func openAIReplayMessage(
	modelCallContextID string,
	replay providerReplayFixture,
) modelcontext.Message {
	return modelcontext.Message{
		Role:                 modelprotocol.RoleAssistant,
		Sequence:             1,
		ModelCallContextID:   modelCallContextID,
		Content:              json.RawMessage(`[]`),
		ProviderReplay:       replay.payload,
		ProviderReplaySource: replay.source,
	}
}

func withToolCallLinks(
	message modelcontext.Message,
	toolCallIDs ...string,
) modelcontext.Message {
	var blocks []json.RawMessage
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		panic(err)
	}
	for _, toolCallID := range toolCallIDs {
		block, err := json.Marshal(map[string]string{
			"type":         "tool_call",
			"tool_call_id": toolCallID,
		})
		if err != nil {
			panic(err)
		}
		blocks = append(blocks, block)
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	message.Content = content
	return message
}

func assistantToolCallMessage(
	modelCallContextID string,
	toolCallIDs ...string,
) modelcontext.Message {
	return withToolCallLinks(modelcontext.Message{
		Role:               modelprotocol.RoleAssistant,
		Sequence:           1,
		ModelCallContextID: modelCallContextID,
		Content:            json.RawMessage(`[]`),
	}, toolCallIDs...)
}
