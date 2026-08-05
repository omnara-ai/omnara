package modelcontext

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
	projected := preparedRequestWithoutInlineMedia(body, media)
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

func TestEstimatePreparedRequestDoesNotInferAbsentMedia(t *testing.T) {
	body := budgetFixtureJSON(t, map[string]any{"input": "small textual fallback"})
	withNoRenderedMedia := EstimatePreparedRequest(body, nil)
	if want := len(body)/4 + 1; withNoRenderedMedia != want {
		t.Fatalf("estimate = %d, want body-only estimate %d", withNoRenderedMedia, want)
	}
}

func TestModelWindowFitsPreparedInputEstimateAtExactBoundary(t *testing.T) {
	window := ModelWindow{
		ContextTokens:          10_000,
		RequestMaxOutputTokens: 2_000,
		SafetyMarginTokens:     1_000,
	}
	if !window.FitsInputEstimate(7_000) {
		t.Fatal("exact usable input boundary should fit")
	}
	if window.FitsInputEstimate(7_001) {
		t.Fatal("input beyond usable boundary should not fit")
	}
}
