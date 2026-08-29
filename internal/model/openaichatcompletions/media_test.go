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

func TestToolResultPreservesResolvedImageAsTextualReference(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"before image"},
		{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
		{"type":"text","text":"after image"}
	]`)
	got := toolResultOutput(modelcontext.ToolResultRef{
		Name:         "inspect_image",
		ContentParts: content,
	})
	if !strings.Contains(got, "before image") || !strings.Contains(got, mediaTestPublicID(mediaTestImageID)) ||
		!strings.Contains(got, "after image") {
		t.Fatalf("resolved image tool result lost content: %q", got)
	}
	if strings.Contains(got, mediaTestImageData) {
		t.Fatalf("text-only tool result unexpectedly inlined image bytes: %q", got)
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
