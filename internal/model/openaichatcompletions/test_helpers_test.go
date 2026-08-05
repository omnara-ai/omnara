package openaichatcompletions

import (
	"encoding/json"
	"net/http/httptest"

	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const (
	testEndpointPath          = "/chat/completions"
	testModelProviderConfigID = "00000000-0000-4000-8000-000000000001"
	mediaTestImageID          = "019b18be-0000-7000-8000-00000000c001"
	mediaTestDocumentID       = "019b18be-0000-7000-8000-00000000c002"
	mediaTestImageData        = "cG5nIGJ5dGVz"
)

func testProviderReplayIdentity(
	providerModelSlug string,
	apiFormat modelprotocol.APIFormat,
	apiVariant modelprotocol.APIVariant,
) modelenvelope.ProviderReplayIdentity {
	return modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      testModelProviderConfigID,
		RequestedProviderModelSlug: providerModelSlug,
		APIFormat:                  apiFormat,
		APIVariant:                 apiVariant,
	}
}

type providerReplayFixture struct {
	source  modelenvelope.ProviderReplayIdentity
	payload json.RawMessage
}

func testProviderReplay(
	providerModelSlug string,
	apiFormat modelprotocol.APIFormat,
	apiVariant modelprotocol.APIVariant,
	payload json.RawMessage,
) providerReplayFixture {
	return providerReplayFixture{
		source:  testProviderReplayIdentity(providerModelSlug, apiFormat, apiVariant),
		payload: payload,
	}
}

func testRespondClient(server *httptest.Server) Client {
	return Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		Auth:                  route.BearerToken{Token: "test-key"},
		BaseURL:               server.URL,
		ProviderModelSlug:     "gpt-test",
		HTTPClient:            server.Client(),
	}
}

func chatReplayMessage(
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

func messageAtSequence(message modelcontext.Message, sequence int64) modelcontext.Message {
	message.Sequence = sequence
	return message
}

func mediaTestResolved() map[string]modelcontext.ResolvedMedia {
	return map[string]modelcontext.ResolvedMedia{
		mediaTestImageID: {
			ArtifactID: mediaTestImageID,
			Kind:       modelcontext.AttachmentKindImage,
			MediaType:  "image/png",
			SizeBytes:  1024,
			Data:       []byte("png bytes"),
		},
		mediaTestDocumentID: {
			ArtifactID: mediaTestDocumentID,
			Kind:       modelcontext.AttachmentKindDocument,
			MediaType:  "application/pdf",
			Filename:   "report.pdf",
			SizeBytes:  2048,
			Data:       []byte("pdf bytes"),
		},
	}
}
