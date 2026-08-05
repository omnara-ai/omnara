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
