package tooloutput

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRewriteOversizedUsesSerializedSizeAndPreservesUTF8(t *testing.T) {
	parts, err := json.Marshal([]map[string]any{{
		"type": "text",
		"text": strings.Repeat("é\x00", 20_000),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	rewritten, err := RewriteOversized(parts, true, func(
		partIndex int,
		contentType string,
		content []byte,
		lineCount int,
	) (Artifact, error) {
		stored = append([]byte(nil), content...)
		return Artifact{
			RawID:       "0198c8b0-0000-7000-8000-000000000042",
			PublicID:    "art_agnmrmyaabyabaaaaaaaaaaaaa",
			ContentType: contentType,
			SizeBytes:   int64(len(content)),
			LineCount:   lineCount,
		}, nil
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if len(rewritten) > MaxInlineToolResultBytes {
		t.Fatalf("serialized result = %d bytes", len(rewritten))
	}
	if string(stored) != strings.Repeat("é\x00", 20_000) {
		t.Fatal("stored content did not preserve the full UTF-8 payload")
	}
	if !json.Valid(rewritten) || !strings.Contains(string(rewritten), `"type":"artifact_ref"`) {
		t.Fatalf("invalid rewrite: %s", rewritten)
	}
}

func TestRewriteOversizedPreservesSmallSiblingParts(t *testing.T) {
	parts, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "keep this note"},
		{"type": "text", "text": strings.Repeat("large output line\n", 6_000)},
		{"type": "structured_data", "value": map[string]any{"ok": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	persistedIndex := -1
	rewritten, err := RewriteOversized(parts, true, func(
		partIndex int,
		contentType string,
		content []byte,
		lineCount int,
	) (Artifact, error) {
		persistedIndex = partIndex
		return Artifact{
			RawID:       "0198c8b0-0000-7000-8000-000000000042",
			PublicID:    "art_agnmrmyaabyabaaaaaaaaaaaaa",
			ContentType: contentType,
			SizeBytes:   int64(len(content)),
			LineCount:   lineCount,
		}, nil
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if persistedIndex != 1 {
		t.Fatalf("persisted part = %d, want largest part 1", persistedIndex)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 4 || decoded[0]["text"] != "keep this note" ||
		decoded[3]["type"] != "structured_data" {
		t.Fatalf("small sibling parts were not preserved: %s", rewritten)
	}
}

func TestRewriteOversizedAccountsForJSONFramingAcrossManyParts(t *testing.T) {
	parts := make([]map[string]any, 1_500)
	for index := range parts {
		parts[index] = map[string]any{"type": "text", "text": "small-value"}
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= MaxInlineToolResultBytes {
		t.Fatalf("test fixture is only %d bytes", len(raw))
	}
	persisted := 0
	rewritten, err := RewriteOversized(raw, true, func(
		_ int,
		contentType string,
		content []byte,
		lineCount int,
	) (Artifact, error) {
		persisted++
		return Artifact{
			RawID:       "0198c8b0-0000-7000-8000-000000000042",
			PublicID:    "art_agnmrmyaabyabaaaaaaaaaaaaa",
			ContentType: contentType,
			SizeBytes:   int64(len(content)),
			LineCount:   lineCount,
		}, nil
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if persisted != 1 || len(rewritten) > MaxInlineToolResultBytes {
		t.Fatalf("persisted=%d rewritten=%d bytes", persisted, len(rewritten))
	}
}

func TestRewriteOversizedPreflightsBeforePersistence(t *testing.T) {
	parts := make([]map[string]any, 0, 2_001)
	parts = append(parts, map[string]any{"type": "text", "text": strings.Repeat("x", 60_000)})
	for range 2_000 {
		parts = append(parts, map[string]any{
			"type":        "media_ref",
			"artifact_id": "0198c8b0-0000-7000-8000-000000000042",
		})
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = RewriteOversized(raw, true, func(int, string, []byte, int) (Artifact, error) {
		called = true
		return Artifact{}, nil
	})
	if !errors.Is(err, ErrCannotBound) {
		t.Fatalf("error = %v, want ErrCannotBound", err)
	}
	if called {
		t.Fatal("persistence ran before the complete replacement was known to fit")
	}
}

func TestPreviewSingleLineUsesBytePaging(t *testing.T) {
	artifact := Artifact{PublicID: "art_example", ContentType: TextContentType}
	preview := Preview([]string{strings.Repeat("x", 20_000)}, 20_000, artifact, "truncated", 200)
	if !strings.Contains(preview, "offset_byte=0") || strings.Contains(preview, "offset_line=2") {
		t.Fatalf("single-line preview guidance = %q", preview)
	}
}
