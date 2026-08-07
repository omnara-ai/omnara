package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolOutputReducerCreatesPreviewWithoutMutatingOriginal(t *testing.T) {
	original := json.RawMessage(`[{"type":"structured_data","value":{"stdout":"abcdefghijklmnopqrstuvwxyz"}}]`)
	reduced, err := (ToolOutputReducer{PreviewBytes: 20}).Reduce(original)
	if err != nil {
		t.Fatalf("reduce tool output: %v", err)
	}
	if string(original) != `[{"type":"structured_data","value":{"stdout":"abcdefghijklmnopqrstuvwxyz"}}]` {
		t.Fatalf("original was mutated: %s", string(original))
	}
	var parts []map[string]any
	if err := json.Unmarshal(reduced, &parts); err != nil {
		t.Fatalf("reduced output is not content parts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("reduced parts = %d, want 1: %s", len(parts), string(reduced))
	}
	omittedBytes, omittedBytesOK := parts[0]["omitted_bytes"].(float64)
	text, textOK := parts[0]["text"].(string)
	if parts[0]["type"] != "text" ||
		parts[0]["reduced"] != true ||
		parts[0]["reducer_version"] != toolOutputReducerVersion ||
		!omittedBytesOK || omittedBytes <= 0 ||
		!textOK ||
		!strings.Contains(text, "omitted") ||
		!strings.HasPrefix(text, `[{"type":"struc`) ||
		!strings.HasSuffix(text, `z"}}]`) {
		t.Fatalf("unexpected reduced parts: %s", string(reduced))
	}
}

func TestProjectionReducerPreservesArtifactRefAndRetrievalClaim(t *testing.T) {
	original := json.RawMessage(`[
		{"type":"structured_data","value":{"outcome":"succeeded"}},
		{"type":"text","text":"` + strings.Repeat("line\\n", 4000) + `"},
		{"type":"artifact_ref","artifact_id":"0198c8b0-0000-7000-8000-000000000042",` +
		`"content_type":"text/plain; charset=utf-8","size_bytes":24000,"line_count":4000}
	]`)
	reduced, changed, err := (ToolResultProjectionReducer{MaxResultBytes: 4_096}).Reduce(original, "web_fetch")
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if !changed || len(reduced) > 4_096 {
		t.Fatalf("reduced=%v bytes=%d", changed, len(reduced))
	}
	if !strings.Contains(string(reduced), `"outcome":"succeeded"`) ||
		!strings.Contains(string(reduced), `"type":"artifact_ref"`) ||
		!strings.Contains(string(reduced), "remains retrievable") ||
		strings.Contains(string(reduced), "not retrievable") {
		t.Fatalf("unexpected projected result: %s", reduced)
	}
}

func TestProjectionReducerAlsoBoundsPullTools(t *testing.T) {
	original, err := json.Marshal([]map[string]any{{
		"type":  "structured_data",
		"value": map[string]any{"content": strings.Repeat("x", 70_000)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reducer := ProjectionReducerForWindow(ModelWindow{
		ContextTokens:          8_000,
		RequestMaxOutputTokens: 2_000,
		SafetyMarginTokens:     1_000,
	})
	reduced, changed, err := reducer.Reduce(original, "read_artifact")
	if err != nil {
		t.Fatalf("reduce pull result: %v", err)
	}
	if !changed || len(reduced) > reducer.MaxResultBytes ||
		!strings.Contains(string(reduced), "Call read_artifact again") {
		t.Fatalf("pull result was not safely projected: %s", reduced)
	}
}
