package modelcontext

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func budgetFixtureJSON(t testing.TB, value any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal budget fixture JSON: %v", err)
	}
	return body
}

func TestEstimatePreparedRequestCountsProtocolFramingWithoutChargingBase64AsText(t *testing.T) {
	data := bytes.Repeat([]byte("image-bytes"), 8_192)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"model": "test-model",
		"input": []map[string]string{
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
		},
		"tools": []map[string]any{{
			"name": "read_file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]string{"type": "string"}},
			},
		}},
	})
	media := ResolvedMedia{
		Kind:      AttachmentKindImage,
		MediaType: "image/png",
		SizeBytes: int64(len(data)),
		Data:      data,
	}
	estimate := EstimatePreparedRequest(body, []RenderedMedia{
		{Media: media, Representation: MediaRepresentationInline},
		{Media: media, Representation: MediaRepresentationInline},
	})
	if estimate < 2*DefaultImageTokenEstimate {
		t.Fatalf("prepared request estimate = %d, want both image occurrences charged", estimate)
	}
	if estimate >= len(body)/4 {
		t.Fatalf("prepared request estimate = %d, raw base64 byte estimate = %d", estimate, len(body)/4)
	}
}

func TestPreparedRequestBudgetRemovesOnlyStructuredMediaFields(t *testing.T) {
	data := bytes.Repeat([]byte("same-as-user-text"), 512)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"input": []map[string]any{
			{"type": "input_text", "text": encoded},
			{"type": "input_image", "image_url": "data:image/png;base64," + encoded},
		},
	})
	media := []RenderedMedia{{
		Media: ResolvedMedia{
			Kind:      AttachmentKindImage,
			MediaType: "image/png",
			SizeBytes: int64(len(data)),
			Data:      data,
		},
		Representation: MediaRepresentationInline,
	}}
	projected := projectPreparedRequest(body, media)
	if !bytes.Contains(projected, []byte(encoded)) {
		t.Fatalf("ordinary user text matching media base64 was removed: %s", projected)
	}
	if bytes.Contains(projected, []byte("data:image/png;base64,"+encoded)) {
		t.Fatalf("structured inline media payload remains in estimate projection: %s", projected)
	}
	if estimate := EstimatePreparedRequest(body, media); estimate <= len(encoded)/4 {
		t.Fatalf("estimate = %d, want matching user text to remain charged", estimate)
	}
}

func TestPreparedRequestBudgetRemovesChatFileData(t *testing.T) {
	data := bytes.Repeat([]byte("pdf-bytes"), 8_192)
	encoded := base64.StdEncoding.EncodeToString(data)
	body := budgetFixtureJSON(t, map[string]any{
		"messages": []map[string]any{{
			"content": []map[string]any{{
				"type": "file",
				"file": map[string]string{
					"filename":  "report.pdf",
					"file_data": "data:application/pdf;base64," + encoded,
				},
			}},
		}},
	})
	media := []RenderedMedia{{
		Media: ResolvedMedia{
			Kind:      AttachmentKindDocument,
			MediaType: "application/pdf",
			Data:      data,
		},
		Representation: MediaRepresentationInline,
	}}
	if estimate := EstimatePreparedRequest(body, media); estimate >= len(body)/4 {
		t.Fatalf("estimate = %d, raw base64 byte estimate = %d", estimate, len(body)/4)
	}
}

func TestEstimatePreparedRequestExcludesDeferredToolsUntilReferenced(t *testing.T) {
	description := strings.Repeat("deferred ", 512)
	largeSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]string{"type": "string", "description": description}},
	}
	loadedTool := map[string]any{"name": "get_time", "input_schema": map[string]string{"type": "object"}}
	deferredTool := map[string]any{"name": "get_weather", "input_schema": largeSchema, "defer_loading": true}
	unloaded := budgetFixtureJSON(t, map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"tools":    []map[string]any{loadedTool, deferredTool},
	})
	withoutDeferred := budgetFixtureJSON(t, map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"tools":    []map[string]any{loadedTool},
	})
	if got, want := EstimatePreparedRequest(unloaded, nil), EstimatePreparedRequest(withoutDeferred, nil); got != want {
		t.Fatalf("unloaded deferred tool estimate = %d, want %d (deferred schema excluded)", got, want)
	}

	referenced := budgetFixtureJSON(t, map[string]any{
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{{
			"type":        "tool_result",
			"tool_use_id": "toolu_1",
			"content":     []map[string]string{{"type": "tool_reference", "tool_name": "get_weather"}},
		}}}},
		"tools": []map[string]any{loadedTool, deferredTool},
	})
	minimum := EstimatePreparedRequest(withoutDeferred, nil) + len(description)/8
	if got := EstimatePreparedRequest(referenced, nil); got <= minimum {
		t.Fatalf("referenced deferred tool estimate = %d, want the loaded schema charged", got)
	}
}

func TestEstimatePreparedRequestDoesNotInferAbsentMedia(t *testing.T) {
	body := budgetFixtureJSON(t, map[string]any{"input": "small textual fallback"})
	withNoRenderedMedia := EstimatePreparedRequest(body, nil)
	if want := len(body)/4 + 1; withNoRenderedMedia != want {
		t.Fatalf("estimate = %d, want body-only estimate %d", withNoRenderedMedia, want)
	}
}

func TestModelWindowComputesExactUsableInputBoundary(t *testing.T) {
	window := ModelWindow{
		ContextTokens:       10_000,
		OutputReserveTokens: 2_000,
		SafetyMarginTokens:  1_000,
	}
	if got := window.UsableInputTokens(); got != 7_000 {
		t.Fatalf("usable input = %d, want 7000", got)
	}
}
