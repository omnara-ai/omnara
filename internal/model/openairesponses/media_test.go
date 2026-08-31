package openairesponses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const (
	mediaTestImageID      = "019b18be-0000-7000-8000-00000000b001"
	mediaTestDocumentID   = "019b18be-0000-7000-8000-00000000b002"
	mediaTestImageData    = "cG5nIGJ5dGVz"
	mediaTestDocumentData = "cGRmIGJ5dGVz"
)

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

func mediaTestPublicID(id string) string {
	return modelcontext.ArtifactPublicID(id)
}

func TestPrepareBuildsInputImageAndFileParts(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"what is in this file?"},
				{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
				{"type":"media_ref","artifact_id":"` + mediaTestDocumentID + `"}
			]`)}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
				Filename string `json:"filename"`
				FileData string `json:"file_data"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Input) != 1 || payload.Input[0].Role != "user" {
		t.Fatalf("unexpected input items: %s", prepared.Body)
	}
	content := payload.Input[0].Content
	if len(content) != 5 ||
		content[0].Type != "input_text" ||
		content[1].Type != "input_text" ||
		content[1].Text != "artifact_id: "+mediaTestPublicID(mediaTestImageID) ||
		content[2].Type != "input_image" ||
		content[3].Type != "input_text" ||
		content[3].Text != "artifact_id: "+mediaTestPublicID(mediaTestDocumentID) ||
		content[4].Type != "input_file" {
		t.Fatalf("unexpected content layout: %s", prepared.Body)
	}
	if content[2].ImageURL != "data:image/png;base64,"+mediaTestImageData {
		t.Fatalf("unexpected image url: %q", content[2].ImageURL)
	}
	if content[4].Filename != "report.pdf" || content[4].FileData != "data:application/pdf;base64,"+mediaTestDocumentData {
		t.Fatalf("unexpected file part: %+v", content[4])
	}
	if strings.Contains(string(prepared.Body), `"media_ref"`) {
		t.Fatalf("resolved media must not keep the textual fallback: %s", prepared.Body)
	}
}

func TestPrepareDerivesDocumentFilenameFromMediaType(t *testing.T) {
	const docxID = "019b18be-0000-7000-8000-00000000b003"
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"review"},
				{"type":"media_ref","artifact_id":"` + docxID + `"}
				]`)}},
			ResolvedMedia: map[string]modelcontext.ResolvedMedia{
				docxID: {
					ArtifactID: docxID,
					Kind:       modelcontext.AttachmentKindDocument,
					MediaType:  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
					SizeBytes:  10,
					Data:       []byte("docx"),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(string(prepared.Body), `"filename":"attachment.docx"`) {
		t.Fatalf("filename must derive its extension from the media type: %s", prepared.Body)
	}
	if strings.Contains(string(prepared.Body), "document.pdf") {
		t.Fatalf("filename must not assume pdf: %s", prepared.Body)
	}
}

func TestPrepareRendersNonResolvedMediaPartsAsText(t *testing.T) {
	// Context building only resolves allowlisted media; everything else
	// reaches the adapter unresolved and keeps the textual ref.
	const svgID = "019b18be-0000-7000-8000-00000000b004"
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"look"},
				{"type":"media_ref","artifact_id":"` + svgID + `"}
			]`)}},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), "input_image") {
		t.Fatalf("non-resolved media must not render as image parts: %s", prepared.Body)
	}
	if !strings.Contains(string(prepared.Body), mediaTestPublicID(svgID)) {
		t.Fatalf("non-resolved media must keep the textual ref: %s", prepared.Body)
	}
}

func TestPrepareCanonicalAssistantMediaRefUsesOutputTextFallback(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{{
				Sequence: 1,
				Role:     modelprotocol.RoleAssistant,
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},{"type":"text","text":"historical media"}]`,
				),
			}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Input) != 1 || payload.Input[0].Role != string(responsesRoleAssistant) ||
		len(payload.Input[0].Content) != 2 {
		t.Fatalf("canonical assistant media payload = %+v, want one assistant message with two parts", payload.Input)
	}
	for _, part := range payload.Input[0].Content {
		if part.Type != string(responsesContentTypeOutputText) {
			t.Fatalf("canonical assistant media content = %+v, want only output_text", payload.Input[0].Content)
		}
	}
	if !strings.Contains(payload.Input[0].Content[0].Text, mediaTestPublicID(mediaTestImageID)) ||
		payload.Input[0].Content[1].Text != "historical media" {
		t.Fatalf("canonical assistant media fallback lost history: %+v", payload.Input[0].Content)
	}
}

func TestPrepareTextOnlyUserMessageStaysOneInputTextPart(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages:     []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)}},
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Input) != 1 || payload.Input[0].Role != "user" {
		t.Fatalf("unexpected input items: %s", prepared.Body)
	}
	content := payload.Input[0].Content
	if len(content) != 1 || content[0].Type != "input_text" || content[0].Text != "hello" {
		t.Fatalf("text-only user message should be one input_text part: %s", prepared.Body)
	}
}

func TestPrepareToolResultMediaRidesInFunctionCallOutput(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{
				Sequence: 1,
				Role:     "user",
				Content:  json.RawMessage(`[{"type":"text","text":"screenshot please"}]`),
			}, assistantToolCallMessage("mcc_1", "tcl_1")},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "screenshot",
				Input:              json.RawMessage(`{}`),
				ContentParts: json.RawMessage(`[
					{"type":"text","text":"captured"},
					{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
				]`),
			}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var output struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload.Input[len(payload.Input)-1], &output); err != nil {
		t.Fatalf("decode output item: %v", err)
	}
	if output.Type != "function_call_output" || output.CallID != "call_1" {
		t.Fatalf("unexpected output item: %+v", output)
	}
	if len(output.Output) != 3 ||
		output.Output[0].Type != "input_text" ||
		output.Output[0].Text != "captured" ||
		output.Output[1].Type != "input_text" ||
		output.Output[1].Text != "artifact_id: "+mediaTestPublicID(mediaTestImageID) ||
		output.Output[2].Type != "input_image" {
		t.Fatalf("expected text and image inside function_call_output: %s", payload.Input[len(payload.Input)-1])
	}
	if output.Output[2].ImageURL != "data:image/png;base64,"+mediaTestImageData {
		t.Fatalf("unexpected image output: %+v", output.Output[2])
	}
	if strings.Contains(string(payload.Input[len(payload.Input)-1]), "media_ref") {
		t.Fatalf("function_call_output must not carry the media textual fallback: %s", payload.Input[len(payload.Input)-1])
	}
}

func TestPrepareInterleavesTextAndMediaInOriginalOrder(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"before"},
				{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
				{"type":"text","text":"middle"},
				{"type":"media_ref","artifact_id":"` + mediaTestDocumentID + `"},
				{"type":"text","text":"after"}
			]`)}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Input) != 1 || payload.Input[0].Role != "user" {
		t.Fatalf("unexpected input items: %s", prepared.Body)
	}
	content := payload.Input[0].Content
	wantTypes := []string{
		"input_text", "input_text", "input_image", "input_text", "input_text", "input_file", "input_text",
	}
	if len(content) != len(wantTypes) {
		t.Fatalf("want %d content parts, got %d: %s", len(wantTypes), len(content), prepared.Body)
	}
	for i, want := range wantTypes {
		if content[i].Type != want {
			t.Fatalf("content[%d] type = %q, want %q: %s", i, content[i].Type, want, prepared.Body)
		}
	}
	if content[0].Text != "before" || content[3].Text != "middle" || content[6].Text != "after" {
		t.Fatalf("text content lost identity across interleaving: %s", prepared.Body)
	}
}

func TestPrepareInterleavedImageThenTextThenImage(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
				{"type":"text","text":"compare these"},
				{"type":"media_ref","artifact_id":"` + mediaTestDocumentID + `"}
			]`)}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	content := payload.Input[0].Content
	if len(content) != 5 || content[0].Type != "input_text" || content[1].Type != "input_image" ||
		content[2].Type != "input_text" || content[3].Type != "input_text" || content[4].Type != "input_file" {
		t.Fatalf("media-first interleaving must round-trip: %s", prepared.Body)
	}
	if content[2].Text != "compare these" {
		t.Fatalf("interleaved text lost: %s", prepared.Body)
	}
}

func TestPrepareInterleavedToolResultPreservesOrder(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"capture"}]`)},
				assistantToolCallMessage("mcc_1", "tcl_1"),
			},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "screenshot",
				Input:              json.RawMessage(`{}`),
				ContentParts: json.RawMessage(`[
					{"type":"text","text":"before"},
					{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
					{"type":"text","text":"after"}
				]`),
			}},
			ResolvedMedia: mediaTestResolved(),
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var output struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload.Input[len(payload.Input)-1], &output); err != nil {
		t.Fatalf("decode output item: %v", err)
	}
	if output.Type != "function_call_output" || output.CallID != "call_1" {
		t.Fatalf("unexpected output item: %+v", output)
	}
	want := []string{"input_text", "input_text", "input_image", "input_text"}
	if len(output.Output) != len(want) {
		t.Fatalf("want %d output parts, got %d: %s", len(want), len(output.Output), payload.Input[len(payload.Input)-1])
	}
	for i, kind := range want {
		if output.Output[i].Type != kind {
			t.Fatalf("output[%d] type = %q, want %q: %s", i, output.Output[i].Type, kind, payload.Input[len(payload.Input)-1])
		}
	}
	if output.Output[0].Text != "before" || output.Output[3].Text != "after" {
		t.Fatalf("interleaved text lost in function_call_output: %s", payload.Input[len(payload.Input)-1])
	}
}
