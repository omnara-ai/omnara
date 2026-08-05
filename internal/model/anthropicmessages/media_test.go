package anthropicmessages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
)

const (
	mediaTestImageID      = "019b18be-0000-7000-8000-00000000a001"
	mediaTestDocumentID   = "019b18be-0000-7000-8000-00000000a002"
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

func TestPrepareBuildsImageAndDocumentBlocks(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
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
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("expected one message, got %+v", payload.Messages)
	}
	content := payload.Messages[0].Content
	if len(content) != 3 || content[0].Type != "text" || content[1].Type != "image" || content[2].Type != "document" {
		t.Fatalf("unexpected block layout: %s", prepared.Body)
	}
	if content[1].Source.Type != "base64" ||
		content[1].Source.MediaType != "image/png" ||
		content[1].Source.Data != mediaTestImageData {
		t.Fatalf("unexpected image source: %+v", content[1].Source)
	}
	if content[2].Source.MediaType != "application/pdf" || content[2].Source.Data != mediaTestDocumentData {
		t.Fatalf("unexpected document source: %+v", content[2].Source)
	}
	minimumEstimate := model.AnthropicImageTokenEstimate(
		"claude-test",
		mediaTestResolved()[mediaTestImageID],
	) + 2048/4
	if prepared.InputTokenEstimate < minimumEstimate {
		t.Fatalf("binary media estimate = %d, want image and PDF charges", prepared.InputTokenEstimate)
	}
}

func TestPrepareDropsOldestMediaToFitProviderBodyLimit(t *testing.T) {
	const (
		oldID = "019b18be-0000-7000-8000-00000000a011"
		newID = "019b18be-0000-7000-8000-00000000a012"
	)
	oldData := bytes.Repeat([]byte("o"), 512)
	newData := bytes.Repeat([]byte("n"), 512)
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{
				Role:     "user",
				Sequence: 1,
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + oldID + `"}]`,
				),
			},
			{
				Role:     "user",
				Sequence: 2,
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + newID + `"}]`,
				),
			},
		},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{
			oldID: {
				ArtifactID: oldID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				Data:       oldData,
			},
			newID: {
				ArtifactID: newID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				Data:       newData,
			},
		},
	}
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	policy := model.RequestPolicy{MaxOutputTokens: 64}

	oneMedia := bundle
	oneMedia.ResolvedMedia = map[string]modelcontext.ResolvedMedia{newID: bundle.ResolvedMedia[newID]}
	oneMediaPrepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: oneMedia, Policy: policy},
		1_000_000,
	)
	if err != nil {
		t.Fatalf("prepare one-media boundary: %v", err)
	}
	prepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: bundle, Policy: policy},
		len(oneMediaPrepared.Body),
	)
	if err != nil {
		t.Fatalf("prepare bounded media: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, base64.StdEncoding.EncodeToString(oldData)) {
		t.Fatalf("oldest media should be removed from bounded request: %s", body)
	}
	if !strings.Contains(body, base64.StdEncoding.EncodeToString(newData)) {
		t.Fatalf("newest media should remain in bounded request: %s", body)
	}
	if !strings.Contains(body, oldID) {
		t.Fatalf("removed media must remain as a textual reference: %s", body)
	}
	if len(prepared.Body) > len(oneMediaPrepared.Body) {
		t.Fatalf("bounded body bytes = %d, limit %d", len(prepared.Body), len(oneMediaPrepared.Body))
	}
}

func TestPrepareDropsOneHistoricalOccurrenceWhenOpeningReusesArtifact(t *testing.T) {
	const artifactID = "019b18be-0000-7000-8000-00000000a013"
	openingInputID, err := storage.ParseID("019b18be-0000-7000-8000-00000000b013")
	if err != nil {
		t.Fatalf("parse opening input id: %v", err)
	}
	data := bytes.Repeat([]byte("r"), 512)
	bundle := modelcontext.Bundle{
		OpeningInputIDs: []storage.ID{openingInputID},
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"media_ref","artifact_id":"` + artifactID + `"}]`)},
			{Role: modelprotocol.RoleUser, Sequence: 2, Content: json.RawMessage(`[{"type":"media_ref","artifact_id":"` + artifactID + `"}]`)},
			{Role: modelprotocol.RoleUser, Sequence: 3, Content: json.RawMessage(`[{"type":"media_ref","artifact_id":"` + artifactID + `"}]`)},
			{
				AgentInputID: openingInputID.String(),
				Role:         modelprotocol.RoleUser,
				Sequence:     4,
				Content:      json.RawMessage(`[{"type":"media_ref","artifact_id":"` + artifactID + `"}]`),
			},
		},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{
			artifactID: {
				ArtifactID: artifactID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				Data:       data,
			},
		},
	}
	policy := model.RequestPolicy{MaxOutputTokens: 64}
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	target := bundle
	target.Messages = append([]modelcontext.Message(nil), bundle.Messages...)
	occurrences := modelcontext.ResolvedMediaOccurrences(target)
	if len(occurrences) != 4 {
		t.Fatalf("resolved occurrences = %+v, want four", occurrences)
	}
	if err := modelcontext.ReplaceMediaOccurrenceWithText(&target, occurrences[0].Ref); err != nil {
		t.Fatalf("prepare target projection: %v", err)
	}
	targetPrepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: target, Policy: policy},
		1_000_000,
	)
	if err != nil {
		t.Fatalf("prepare target body: %v", err)
	}

	prepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: bundle, Policy: policy},
		len(targetPrepared.Body),
	)
	if err != nil {
		t.Fatalf("prepare repeated artifact body: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if got := strings.Count(string(prepared.Body), encoded); got != 3 {
		t.Fatalf("rendered repeated-artifact occurrences = %d, want three: %s", got, prepared.Body)
	}
	if !strings.Contains(string(prepared.Body), artifactID) {
		t.Fatalf("dropped historical occurrence must remain textual: %s", prepared.Body)
	}
	if len(prepared.Body) > len(targetPrepared.Body) {
		t.Fatalf("bounded body bytes = %d, limit %d", len(prepared.Body), len(targetPrepared.Body))
	}
}

func TestPrepareNeverDropsOpeningMediaToFitProviderBodyLimit(t *testing.T) {
	const (
		currentID    = "019b18be-0000-7000-8000-00000000a021"
		historicalID = "019b18be-0000-7000-8000-00000000a022"
	)
	openingInputID, err := storage.ParseID("019b18be-0000-7000-8000-00000000b021")
	if err != nil {
		t.Fatalf("parse opening input id: %v", err)
	}
	currentData := bytes.Repeat([]byte("c"), 512)
	historicalData := bytes.Repeat([]byte("h"), 512)
	bundle := modelcontext.Bundle{
		OpeningInputIDs: []storage.ID{openingInputID},
		Messages: []modelcontext.Message{
			{
				Role:         "user",
				Sequence:     1,
				AgentInputID: openingInputID.String(),
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + currentID + `"}]`,
				),
			},
			{
				Role:     "user",
				Sequence: 2,
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + historicalID + `"}]`,
				),
			},
		},
		ResolvedMedia: map[string]modelcontext.ResolvedMedia{
			currentID: {
				ArtifactID: currentID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				Data:       currentData,
			},
			historicalID: {
				ArtifactID: historicalID,
				Kind:       modelcontext.AttachmentKindImage,
				MediaType:  "image/png",
				Data:       historicalData,
			},
		},
	}
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	policy := model.RequestPolicy{MaxOutputTokens: 64}
	currentOnly := bundle
	currentOnly.ResolvedMedia = map[string]modelcontext.ResolvedMedia{
		currentID: bundle.ResolvedMedia[currentID],
	}
	currentPrepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: currentOnly, Policy: policy},
		1_000_000,
	)
	if err != nil {
		t.Fatalf("prepare opening media boundary: %v", err)
	}

	prepared, err := client.prepareWithinRequestBodyLimit(
		context.Background(),
		model.PrepareInput{Context: bundle, Policy: policy},
		len(currentPrepared.Body),
	)
	if err != nil {
		t.Fatalf("prepare protected opening media: %v", err)
	}
	body := string(prepared.Body)
	if !strings.Contains(body, base64.StdEncoding.EncodeToString(currentData)) {
		t.Fatalf("opening media was removed from bounded request: %s", body)
	}
	if strings.Contains(body, base64.StdEncoding.EncodeToString(historicalData)) {
		t.Fatalf("historical media should be removed before opening media: %s", body)
	}
}

func TestPrepareRejectsOpeningImageAboveAnthropicPerImageLimit(t *testing.T) {
	const currentID = "019b18be-0000-7000-8000-00000000a023"
	openingInputID, err := storage.ParseID("019b18be-0000-7000-8000-00000000b023")
	if err != nil {
		t.Fatalf("parse opening input id: %v", err)
	}
	data := bytes.Repeat([]byte("x"), anthropicMessagesImageBase64Limit*3/4+1)
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	_, err = client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			OpeningInputIDs: []storage.ID{openingInputID},
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user",
				AgentInputID: openingInputID.String(),
				Content: json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + currentID + `"}]`,
				),
			}},
			ResolvedMedia: map[string]modelcontext.ResolvedMedia{
				currentID: {
					ArtifactID: currentID,
					Kind:       modelcontext.AttachmentKindImage,
					MediaType:  "image/png",
					Data:       data,
				},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindInvalidRequest ||
		!strings.Contains(providerErr.Message, "base64 image limit") {
		t.Fatalf("oversized opening image = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestPrepareRendersNonResolvedMediaPartsAsText(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"hello"},
				{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
			]`)}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(string(prepared.Body), `"type":"image"`) {
		t.Fatalf("non-resolved media must not produce image blocks: %s", prepared.Body)
	}
	if !strings.Contains(string(prepared.Body), "hello") {
		t.Fatalf("text content lost: %s", prepared.Body)
	}
	if !strings.Contains(string(prepared.Body), mediaTestImageID) {
		t.Fatalf("non-resolved media_ref must keep its textual form: %s", prepared.Body)
	}
}

func TestPrepareToolResultWithImageUsesContentBlocks(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"take a screenshot"}]`)},
				messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
			},
			ToolResults: []modelcontext.ToolResultRef{{SourceEventSequence: 2, ToolCallID: "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "screenshot",
				Input:              json.RawMessage(`{}`),
				ContentParts: json.RawMessage(`[
					{"type":"text","text":"captured"},
					{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"},
					{"type":"media_ref","artifact_id":"` + mediaTestDocumentID + `"}
				]`),
			}},
			ResolvedMedia: mediaTestResolved(),
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(payload.Messages), prepared.Body)
	}
	toolResult := string(payload.Messages[2].Content[0])
	if !strings.Contains(toolResult, `"type":"image"`) || !strings.Contains(toolResult, mediaTestImageData) {
		t.Fatalf("tool result missing image block: %s", toolResult)
	}
	if !strings.Contains(toolResult, `"type":"document"`) || !strings.Contains(toolResult, mediaTestDocumentData) {
		t.Fatalf("tool result missing document block: %s", toolResult)
	}
	if len(payload.Messages[2].Content) != 1 {
		t.Fatalf("tool result media should stay inside the tool_result block: %s", prepared.Body)
	}
}

func TestPrepareAssistantMediaStaysTextual(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"draw a cat"}]`)},
				{Sequence: 2, Role: "assistant", Content: json.RawMessage(`[
					{"type":"text","text":"here is your cat"},
					{"type":"media_ref","artifact_id":"` + mediaTestImageID + `"}
				]`)},
			},
			ResolvedMedia: mediaTestResolved(),
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 2 || payload.Messages[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %s", prepared.Body)
	}
	assistant := ""
	for _, block := range payload.Messages[1].Content {
		assistant += string(block)
	}
	if strings.Contains(assistant, `"type":"image"`) {
		t.Fatalf("assistant turns must not carry image blocks: %s", assistant)
	}
	if !strings.Contains(assistant, mediaTestImageID) {
		t.Fatalf("assistant media_ref must keep its textual form: %s", assistant)
	}
}

func TestPrepareRendersTextDocumentsAsPlainTextSource(t *testing.T) {
	const markdownID = "019b18be-0000-7000-8000-00000000a003"
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"summarize"},
				{"type":"media_ref","artifact_id":"` + markdownID + `"}
				]`)}},
			ResolvedMedia: map[string]modelcontext.ResolvedMedia{
				markdownID: {
					ArtifactID: markdownID,
					Kind:       modelcontext.AttachmentKindDocument,
					MediaType:  "text/markdown",
					Filename:   "notes.md",
					SizeBytes:  4 * 1024 * 1024,
					Data:       []byte("# notes"),
				},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if !strings.Contains(body, `"type":"text","media_type":"text/plain","data":"# notes"`) {
		t.Fatalf("text document must render as a plain-text source: %s", body)
	}
	if strings.Contains(body, `"type":"base64","media_type":"text/markdown"`) {
		t.Fatalf("text documents must not render as base64 sources: %s", body)
	}
	if prepared.InputTokenEstimate >= 10_000 {
		t.Fatalf("text document was charged again outside serialized text: %d", prepared.InputTokenEstimate)
	}
}

func TestPrepareDegradesUnsupportedDocumentTypesToText(t *testing.T) {
	const docxID = "019b18be-0000-7000-8000-00000000a004"
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{{Sequence: 1, Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"review the doc"},
				{"type":"media_ref","artifact_id":"` + docxID + `"}
				]`)}},
			ResolvedMedia: map[string]modelcontext.ResolvedMedia{
				docxID: {
					ArtifactID: docxID,
					Kind:       modelcontext.AttachmentKindDocument,
					MediaType:  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
					Filename:   "spec.docx",
					SizeBytes:  4 * 1024 * 1024,
					Data:       []byte("docx"),
				},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	body := string(prepared.Body)
	if strings.Contains(body, `"type":"document"`) {
		t.Fatalf("unsupported document types must not render as document blocks: %s", body)
	}
	if !strings.Contains(body, docxID) {
		t.Fatalf("unsupported document types must keep the textual ref: %s", body)
	}
	if prepared.InputTokenEstimate >= 10_000 {
		t.Fatalf("unsupported document fallback was charged as binary media: %d", prepared.InputTokenEstimate)
	}
}

func TestPrepareInterleavesTextAndMediaInOriginalOrder(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
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
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("expected one message, got %d: %s", len(payload.Messages), prepared.Body)
	}
	content := payload.Messages[0].Content
	wantTypes := []string{"text", "image", "text", "document", "text"}
	if len(content) != len(wantTypes) {
		t.Fatalf("want %d blocks (text/image/text/document/text), got %d: %s", len(wantTypes), len(content), prepared.Body)
	}
	for i, want := range wantTypes {
		if content[i].Type != want {
			t.Fatalf("block[%d] type = %q, want %q: %s", i, content[i].Type, want, prepared.Body)
		}
	}
	if content[0].Text != "before" || content[2].Text != "middle" || content[4].Text != "after" {
		t.Fatalf("text blocks lost identity in interleaved order: %s", prepared.Body)
	}
}

func TestPrepareInterleavedImageThenTextThenImage(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
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
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	content := payload.Messages[0].Content
	if len(content) != 3 || content[0].Type != "image" || content[1].Type != "text" || content[2].Type != "document" {
		t.Fatalf("media-first interleaving must round-trip: %s", prepared.Body)
	}
	if content[1].Text != "compare these" {
		t.Fatalf("interleaved text lost: %s", prepared.Body)
	}
}

func TestPrepareInterleavedToolResultPreservesOrder(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"capture"}]`)},
				messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
			},
			ToolResults: []modelcontext.ToolResultRef{{SourceEventSequence: 2, ToolCallID: "tcl_1",
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
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(payload.Messages), prepared.Body)
	}
	var toolResult struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload.Messages[2].Content[0], &toolResult); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if toolResult.Type != "tool_result" {
		t.Fatalf("unexpected wrapper: %s", payload.Messages[2].Content[0])
	}
	if len(toolResult.Content) != 3 {
		t.Fatalf(
			"want text/image/text inside tool_result, got %d blocks: %s",
			len(toolResult.Content),
			payload.Messages[2].Content[0],
		)
	}
	if toolResult.Content[0].Type != "text" || toolResult.Content[1].Type != "image" ||
		toolResult.Content[2].Type != "text" {
		t.Fatalf("tool result interleaving destroyed: %s", payload.Messages[2].Content[0])
	}
	if toolResult.Content[0].Text != "before" || toolResult.Content[2].Text != "after" {
		t.Fatalf("tool result text blocks lost identity: %s", payload.Messages[2].Content[0])
	}
}

func TestPrepareTextOnlyToolResultEmitsOneTextBlock(t *testing.T) {
	client := Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "sys",
			Messages: []modelcontext.Message{
				{Sequence: 1, Role: "user", Content: json.RawMessage(`[{"type":"text","text":"run"}]`)},
				messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
			},
			ToolResults: []modelcontext.ToolResultRef{{SourceEventSequence: 2, ToolCallID: "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "run_command",
				Input:              json.RawMessage(`{}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"done"}]`),
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("expected user/assistant/user-tool_result triple, got %d: %s", len(payload.Messages), prepared.Body)
	}
	var toolResult struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload.Messages[2].Content[0], &toolResult); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if toolResult.Type != "tool_result" {
		t.Fatalf("unexpected wrapper: %s", payload.Messages[2].Content[0])
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].Type != "text" || toolResult.Content[0].Text != "done" {
		t.Fatalf("text-only tool result must be one text block: %s", payload.Messages[2].Content[0])
	}
}
