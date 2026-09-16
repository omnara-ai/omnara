package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestToolResultOverflowPreservesContentAndAttachments(t *testing.T) {
	artifactID, imageID := uuid.New(), uuid.New()
	manyParts := make([]map[string]any, 0, 2000)
	for range 2000 {
		manyParts = append(manyParts, map[string]any{"type": "text", "text": "small block with some content"})
	}
	for _, test := range []struct {
		name        string
		parts       json.RawMessage
		contentType string
		hidden      bool
	}{
		{
			name:        "text",
			contentType: "text/plain",
			parts:       mustTestRawJSON(t, []map[string]any{{"type": "text", "text": strings.Repeat("é\t", 30000)}}),
		},
		{
			name: "structured data with media",
			parts: mustTestRawJSON(t, []map[string]any{
				{"type": "structured_data", "value": map[string]any{"large": strings.Repeat("x", 60000)}},
				{"type": "media_ref", "artifact_id": imageID.String()},
			}),
		},
		{name: "many text blocks", parts: mustTestRawJSON(t, manyParts)},
		{name: "text with block metadata", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("x", 60000),
				"metadata": map[string]string{"source": "crm", "omnara_hidden": "false"}},
		})},
		{name: "hidden text", hidden: true, parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("x", 60000),
				"metadata": map[string]string{"omnara_hidden": "true"}},
		})},
		{name: "hidden structured data", hidden: true, parts: mustTestRawJSON(t, []map[string]any{
			{"type": "structured_data", "value": strings.Repeat("x", 60000),
				"metadata": map[string]string{"omnara_hidden": "true"}},
		})},
		{name: "mixed visible and hidden bundle", hidden: true, parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("visible", 10000)},
			{"type": "text", "text": strings.Repeat("hidden", 10000), "metadata": map[string]string{"omnara_hidden": "true"}},
			{"type": "media_ref", "artifact_id": imageID.String()},
		})},
		{name: "bundle with hidden media", hidden: true, parts: mustTestRawJSON(t, []map[string]any{
			{"type": "structured_data", "value": strings.Repeat("x", 60000)},
			{"type": "media_ref", "artifact_id": imageID.String(), "metadata": map[string]string{"omnara_hidden": "true"}},
		})},
		{name: "plaintext with retained hidden blocks", contentType: "text/plain", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("visible", 10000)},
			{"type": "structured_data", "value": "hidden", "metadata": map[string]string{"omnara_hidden": "true"}},
			{"type": "media_ref", "artifact_id": imageID.String(), "metadata": map[string]string{"omnara_hidden": "true"}},
		})},
		{name: "text with metadata", contentType: "text/plain", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
			{"type": "structured_data", "value": map[string]any{"status_code": 200}},
		})},
		{name: "metadata before text", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "structured_data", "value": map[string]any{"status_code": 200}},
			{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
		})},
		{name: "escaped metadata fits inline", contentType: "text/plain", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
			{"type": "structured_data", "value": strings.Repeat("\t", 20900)},
			{"type": "media_ref", "artifact_id": imageID.String()},
		})},
		{name: "escaped metadata exceeds inline budget", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
			{"type": "structured_data", "value": strings.Repeat("\t", 21000)},
		})},
		{name: "multiple large blocks", parts: mustTestRawJSON(t, []map[string]any{
			{"type": "text", "text": strings.Repeat("TARGET é\n", 8000)},
			{"type": "structured_data", "value": map[string]any{"number": 1, "large": strings.Repeat("x", 60000)}},
			{"type": "media_ref", "artifact_id": imageID.String()},
			{"type": "text", "text": "last block"},
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			parts := test.parts
			inputBlocks, err := parseToolResultContentBlocks(parts)
			if err != nil {
				t.Fatal(err)
			}
			overflow, err := prepareToolResultOverflow(inputBlocks, parts)
			if err != nil || overflow.content == nil {
				t.Fatalf("prepare overflow: %v", err)
			}
			wantType := test.contentType
			if wantType == "" {
				wantType = "application/json"
			}
			if overflow.contentType != wantType {
				t.Fatalf("content type=%s, want %s", overflow.contentType, wantType)
			}
			saved := overflow.content
			result, err := overflow.contentParts(artifactID)
			if err != nil {
				t.Fatal(err)
			}
			if len(saved) < ToolResultInlineBudgetBytes || len(result) > ToolResultInlineBudgetBytes {
				t.Fatalf("saved=%d result=%d", len(saved), len(result))
			}
			blocks, err := parseToolResultContentBlocks(result)
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) < 2 || blocks[1].ArtifactID != artifactID || !blocks[1].ExcludeFromModelContext {
				t.Fatal("missing downloadable attachment")
			}
			for _, block := range blocks[:2] {
				if hidden := block.Metadata["omnara_hidden"] == "true"; hidden != test.hidden {
					t.Fatalf("generated %s block hidden=%t, want %t", block.BlockKind, hidden, test.hidden)
				}
			}
			if bytes.Contains(parts, []byte(imageID.String())) && !bytes.Contains(result, []byte(imageID.String())) {
				t.Fatal("lost existing media")
			}
			repeated, err := prepareToolResultOverflow(inputBlocks, parts)
			if err != nil || repeated.content == nil || !bytes.Equal(repeated.content, saved) {
				t.Fatalf("changed retained content: %v", err)
			}
			replay, err := repeated.contentParts(artifactID)
			if err != nil || !bytes.Equal(result, replay) {
				t.Fatal("rewrite is not deterministic")
			}
			ctx := context.WithValue(t.Context(), toolResultArtifactContentsKey{}, map[uuid.UUID][]byte{artifactID: saved})
			expanded, err := expandToolResultOverflow(ctx, uuid.New(), uuid.New(), result)
			if err != nil || !sameJSON(expanded, parts) {
				t.Fatalf("round trip changed original parts: %v", err)
			}
			if test.name == "text with metadata" && len(regexp.MustCompile(`(?m)^TARGET`).FindAll(saved, -1)) != 8000 {
				t.Fatal("artifact lost original text lines")
			}
			if !bytes.Contains(result, []byte("/artifacts/")) {
				t.Fatal("missing retrieval path")
			}
		})
	}
}

func TestToolResultOverflowPreservesMediaAboveInlineLimit(t *testing.T) {
	blocks := []CreateContentBlockInput{{
		BlockKind: ContentBlockKindText, TextContent: strings.Repeat("x", ToolResultInlineBudgetBytes+1),
	}}
	for range 1000 {
		blocks = append(blocks, CreateContentBlockInput{
			BlockKind: ContentBlockKindArtifact, ArtifactID: uuid.New(),
		})
	}
	parts, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		t.Fatal(err)
	}
	overflow, err := prepareToolResultOverflow(blocks, parts)
	if err != nil || overflow.content == nil || overflow.contentType != "application/json" {
		t.Fatalf("prepare mixed-media overflow: %v", err)
	}
	artifactID := uuid.New()
	result, err := overflow.contentParts(artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) <= ToolResultInlineBudgetBytes {
		t.Fatalf("expected retained media to exceed inline limit, got %d bytes", len(result))
	}
	retained, err := parseToolResultContentBlocks(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != len(blocks)+1 {
		t.Fatalf("got %d blocks, want preview, overflow attachment, and all original media", len(retained))
	}
	for index, block := range blocks[1:] {
		if retained[index+2].ArtifactID != block.ArtifactID {
			t.Fatalf("media reference %d was changed or lost", index)
		}
	}
	ctx := context.WithValue(t.Context(), toolResultArtifactContentsKey{}, map[uuid.UUID][]byte{
		artifactID: overflow.content,
	})
	expanded, err := expandToolResultOverflow(ctx, uuid.New(), uuid.New(), result)
	if err != nil || !sameJSON(expanded, parts) {
		t.Fatalf("round trip changed original mixed-media content: %v", err)
	}
}

func TestSmallAndMediaOnlyResultsStayUnchanged(t *testing.T) {
	mediaParts := make([]map[string]any, 0, 1000)
	for range 1000 {
		mediaParts = append(mediaParts, map[string]any{"type": "media_ref", "artifact_id": uuid.NewString()})
	}
	for _, test := range []struct {
		name  string
		parts json.RawMessage
	}{
		{name: "small text", parts: json.RawMessage(`[{"type":"text","text":"small"}]`)},
		{name: "media only", parts: mustTestRawJSON(t, mediaParts)},
	} {
		t.Run(test.name, func(t *testing.T) {
			blocks, err := parseToolResultContentBlocks(test.parts)
			if err != nil {
				t.Fatal(err)
			}
			overflow, err := prepareToolResultOverflow(blocks, test.parts)
			if err != nil || overflow.content != nil {
				t.Fatalf("unexpected overflow: %v", err)
			}
		})
	}
}

func TestToolResultOverflowRejectsUnreadableSize(t *testing.T) {
	parts := mustTestRawJSON(t, []map[string]any{{
		"type": "text", "text": strings.Repeat("x", toolcatalog.MaxReadableArtifactBytes+1),
	}})
	blocks, err := parseToolResultContentBlocks(parts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareToolResultOverflow(blocks, parts)
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("size error must be non-retryable: %v", err)
	}
}
