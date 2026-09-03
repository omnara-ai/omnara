package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestInlineMediaPreflightDecodesOnceIntoReusablePlan(t *testing.T) {
	payload := []byte("decoded once")
	content := json.RawMessage(`[{"type":"media","media_type":"text/plain","data":"` +
		base64.StdEncoding.EncodeToString(payload) + `"}]`)

	plan, err := preflightInlineMedia(content, inlineMediaAgentInput, 0)
	if err != nil {
		t.Fatalf("preflight inline media: %v", err)
	}
	attachment, ok := plan.attachments[0]
	if !plan.validated || !ok || string(attachment.content) != string(payload) {
		t.Fatalf("inline media plan = %+v", plan)
	}
}

func TestInlineMediaPreflightRejectsWholeInputBeforeMaterialization(t *testing.T) {
	validMedia := base64.StdEncoding.EncodeToString([]byte("valid"))
	tests := map[string]json.RawMessage{
		"invalid base64": json.RawMessage(
			`[{"type":"media","media_type":"text/plain","data":"not base64"}]`,
		),
		"invalid later text": json.RawMessage(
			`[{"type":"media","media_type":"text/plain","data":"` + validMedia +
				`"},{"type":"text"}]`,
		),
		"oversized text": json.RawMessage(
			`[{"type":"text","text":"` + strings.Repeat("x", maxContentBlockBytes+1) + `"}]`,
		),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			plan, err := preflightInlineMedia(content, inlineMediaAgentInput, 0)
			if err == nil || plan.validated || len(plan.attachments) != 0 {
				t.Fatalf("preflight plan = %+v, error = %v", plan, err)
			}
		})
	}
}

func TestInlineMediaPreflightCapsContentBlockCount(t *testing.T) {
	blocks := make([]json.RawMessage, maxContentBlocksPerInput+1)
	for i := range blocks {
		blocks[i] = json.RawMessage(`{"type":"text","text":"ok"}`)
	}
	content, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal content blocks: %v", err)
	}
	if _, err := preflightInlineMedia(
		content,
		inlineMediaAgentInput,
		maxContentBlocksPerInput,
	); err == nil ||
		!strings.Contains(err.Error(), "too many content blocks") {
		t.Fatalf("content-block cap error = %v", err)
	}
}

func TestInlineMediaPreflightPreservesToolResultSchema(t *testing.T) {
	media := base64.StdEncoding.EncodeToString([]byte("tool output"))
	content := json.RawMessage(
		`[{"type":"structured_data","value":{"ok":true}},` +
			`{"type":"media","media_type":"text/plain","data":"` + media + `"}]`,
	)
	plan, err := preflightInlineMedia(content, inlineMediaToolResult, 0)
	if err != nil || !plan.validated || len(plan.attachments) != 1 {
		t.Fatalf("tool-result preflight plan = %+v, error = %v", plan, err)
	}

	empty, err := preflightInlineMedia(json.RawMessage(`[]`), inlineMediaToolResult, 0)
	if err != nil || !empty.validated || len(empty.rawBlocks) != 0 {
		t.Fatalf("empty tool-result preflight plan = %+v, error = %v", empty, err)
	}
}
