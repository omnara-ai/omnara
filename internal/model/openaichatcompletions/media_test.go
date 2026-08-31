package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func mediaTestPublicID(id string) string {
	return modelcontext.ArtifactPublicID(id)
}

func TestPrepareBuildsImagePartsAndTextFallbacks(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	resolved := mediaTestResolved()
	document := resolved[mediaTestDocumentID]
	document.SizeBytes = 4 * 1024 * 1024
	resolved[mediaTestDocumentID] = document
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[
				{"type":"text","text":"what is in this file?"},
				{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
				{"type":"media_ref","artifact_id":"` + mediaTestDocumentID + `"}
			]`)}},
			ResolvedMedia: resolved,
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 1 || payload.Messages[0].Role != string(chatRoleUser) {
		t.Fatalf("unexpected messages: %s", prepared.Body)
	}
	content := payload.Messages[0].Content
	if len(content) != 4 || content[0].Type != "text" || content[1].Type != "text" ||
		content[1].Text != "artifact_id: "+mediaTestPublicID(mediaTestImageID) ||
		content[2].Type != "image_url" || content[3].Type != "text" {
		t.Fatalf("unexpected content layout: %s", prepared.Body)
	}
	if content[2].ImageURL.URL != "data:image/png;base64,"+mediaTestImageData {
		t.Fatalf("unexpected image URL: %+v", content[2].ImageURL)
	}
	if !strings.Contains(content[3].Text, mediaTestPublicID(mediaTestDocumentID)) {
		t.Fatalf("document fallback lost media ref: %+v", content[3])
	}
	if prepared.InputTokenEstimate < 25_000 || prepared.InputTokenEstimate >= 30_000 {
		t.Fatalf("prepared estimate = %d, want image charge without PDF fallback bytes", prepared.InputTokenEstimate)
	}
}

func TestPrepareKeepsUnresolvedMediaAsText(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[
			{"type":"text","text":"look"},
			{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
		]`)}},
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, "image_url") {
		t.Fatalf("unresolved media must not render as image_url: %s", prepared.Body)
	}
	if !strings.Contains(body, mediaTestPublicID(mediaTestImageID)) {
		t.Fatalf("unresolved media ref missing textual fallback: %s", prepared.Body)
	}
}

func TestPrepareKeepsAssistantMediaReferenceWhenSwitchingFormats(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{{Sequence: 1, Role: modelprotocol.RoleAssistant, Content: json.RawMessage(`[
			{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
		]`)}},
		ResolvedMedia: mediaTestResolved(),
	}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if !strings.Contains(body, mediaTestPublicID(mediaTestImageID)) {
		t.Fatalf("assistant media reference was lost during canonical format conversion: %s", prepared.Body)
	}
	if strings.Contains(body, "provider_item_id") {
		t.Fatalf("provider-specific media metadata leaked into Chat Completions: %s", prepared.Body)
	}
}

func TestProjectRenderedMediaIncludesToolResultImages(t *testing.T) {
	client := Client{ProviderModelSlug: "gpt-test"}
	rendered := client.ProjectRenderedMedia(modelcontext.Bundle{
		ToolResults: []modelcontext.ToolResultRef{{
			Name:                "inspect_image",
			SourceEventSequence: 1,
			ContentParts: json.RawMessage(
				`[{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}]`,
			),
		}},
		ResolvedMedia: mediaTestResolved(),
	})
	if len(rendered) != 1 || rendered[0].Media.ArtifactID != mediaTestImageID {
		t.Fatalf("rendered media = %+v, want tool-result image", rendered)
	}
}

func TestToolResultOutputOmitsResolvedImageFromText(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"before image"},
		{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
		{"type":"text","text":"after image"}
	]`)
	got := toolResultOutput(modelcontext.ToolResultRef{
		Name:         "inspect_image",
		ContentParts: content,
	}, mediaTestResolved())
	if !strings.Contains(got, "before image") || !strings.Contains(got, "after image") {
		t.Fatalf("resolved image tool result lost content: %q", got)
	}
	if strings.Contains(got, mediaTestPublicID(mediaTestImageID)) || strings.Contains(got, mediaTestImageData) {
		t.Fatalf("resolved image leaked into text-only tool result: %q", got)
	}
}

func TestPrepareAddsToolResultImageAfterAllToolMessages(t *testing.T) {
	client := Client{
		EndpointPath:      testEndpointPath,
		ProviderModelSlug: "gpt-test",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: modelprotocol.RoleUser, Content: json.RawMessage(`[{"type":"text","text":"capture"}]`)},
				messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_text", "tcl_image"), 2),
			},
			ToolResults: []modelcontext.ToolResultRef{
				{
					ToolCallID:         "tcl_text",
					ModelCallContextID: "mcc_1",
					ProviderCallID:     "call_text",
					Name:               "read_status",
					Input:              json.RawMessage(`{}`),
					ContentParts:       json.RawMessage(`[{"type":"text","text":"ready"}]`),
				},
				{
					ToolCallID:         "tcl_image",
					ModelCallContextID: "mcc_1",
					ProviderCallID:     "call_image",
					Name:               "screenshot",
					Input:              json.RawMessage(`{}`),
					ContentParts: json.RawMessage(`[
						{"type":"structured_data","value":{"outcome":"succeeded"}},
						{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
					]`),
				},
			},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 5 ||
		payload.Messages[2].Role != string(chatRoleTool) ||
		payload.Messages[2].ToolCallID != "call_text" ||
		payload.Messages[3].Role != string(chatRoleTool) ||
		payload.Messages[3].ToolCallID != "call_image" ||
		payload.Messages[4].Role != string(chatRoleUser) {
		t.Fatalf("tool-result image message ordering is wrong: %s", prepared.Body)
	}
	if string(payload.Messages[3].Content) != `"{\"outcome\":\"succeeded\"}"` {
		t.Fatalf("image tool result text = %s, want succeeded outcome", payload.Messages[3].Content)
	}
	var content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(payload.Messages[4].Content, &content); err != nil {
		t.Fatalf("decode image message: %v", err)
	}
	if len(content) != 2 || content[0].Type != "text" || content[1].Type != "image_url" ||
		!strings.Contains(content[0].Text, "screenshot") ||
		!strings.Contains(content[0].Text, mediaTestPublicID(mediaTestImageID)) ||
		content[1].ImageURL.URL != "data:image/png;base64,"+mediaTestImageData {
		t.Fatalf("unexpected tool-result image content: %s", payload.Messages[4].Content)
	}
}

func TestOpenRouterFallbackModelsBudgetLargestImageCandidate(t *testing.T) {
	media := resolvedPNGMedia(t, mediaTestImageID, 100, 7_000)
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{{
			Sequence: 1,
			Role:     modelprotocol.RoleUser,
			Content: json.RawMessage(
				`[{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}]`,
			),
		}},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{mediaTestImageID: media},
	}
	client := Client{
		ProviderModelSlug: "anthropic/claude-sonnet-4.6",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"models":["openai/gpt-5.6"]}`),
	}
	rendered := client.ProjectRenderedMedia(bundle)
	fallbackEstimate := model.OpenAIImageTokenEstimate("openai/gpt-5.6", media)
	primaryEstimate := model.OpenAIImageTokenEstimate(client.ProviderModelSlug, media)
	if fallbackEstimate <= primaryEstimate {
		t.Fatalf("fixture fallback estimate = %d, want greater than primary %d", fallbackEstimate, primaryEstimate)
	}
	if len(rendered) != 1 || rendered[0].TokenEstimate != fallbackEstimate {
		t.Fatalf("rendered media = %+v, want fallback estimate %d", rendered, fallbackEstimate)
	}
}

func resolvedPNGMedia(t *testing.T, artifactID string, width, height int) modelcontext.ResolvedMedia {
	t.Helper()
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return modelcontext.ResolvedMedia{
		ArtifactID: artifactID,
		Kind:       modelcontext.AttachmentKindImage,
		MediaType:  "image/png",
		SizeBytes:  int64(body.Len()),
		Data:       body.Bytes(),
	}
}
