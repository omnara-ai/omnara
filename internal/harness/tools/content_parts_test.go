package tools

import (
	"encoding/json"
	"testing"
)

func TestStructuredToolResultContentDoesNotInferContentBlocks(t *testing.T) {
	tests := []struct {
		name  string
		value json.RawMessage
		want  string
	}{
		{
			name:  "null",
			value: json.RawMessage(`null`),
			want:  `[{"type":"structured_data","value":null}]`,
		},
		{
			name:  "empty object",
			value: json.RawMessage(`{}`),
			want:  `[{"type":"structured_data","value":{}}]`,
		},
		{
			name:  "array resembling content blocks",
			value: json.RawMessage(`[{"type":"text","text":"domain data"}]`),
			want: `[{"type":"structured_data","value":` +
				`[{"type":"text","text":"domain data"}]}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := structuredToolResultContent(tt.value)
			if err != nil {
				t.Fatalf("build structured tool result: %v", err)
			}
			got, err := content.contentParts()
			if err != nil {
				t.Fatalf("marshal structured tool result: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("content parts = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestToolResultContentPreservesExplicitPartOrder(t *testing.T) {
	structured, err := structuredToolResultPart(map[string]any{"count": 2})
	if err != nil {
		t.Fatalf("build structured part: %v", err)
	}
	content := newToolResultContent(
		textToolResultPart("first"),
		structured,
		textToolResultPart("last"),
	)
	got, err := content.contentParts()
	if err != nil {
		t.Fatalf("marshal tool result content: %v", err)
	}
	want := `[{"type":"text","text":"first"},` +
		`{"type":"structured_data","value":{"count":2}},` +
		`{"type":"text","text":"last"}]`
	if string(got) != want {
		t.Fatalf("content parts = %s, want %s", got, want)
	}
}

func TestEmptyToolResultContentHasNoBlocks(t *testing.T) {
	got, err := newToolResultContent().contentParts()
	if err != nil {
		t.Fatalf("marshal empty tool result content: %v", err)
	}
	if string(got) != `[]` {
		t.Fatalf("content parts = %s, want []", got)
	}
	if _, err := (toolResultContent{}).contentParts(); err == nil {
		t.Fatal("unset tool result content should be rejected")
	}
}
