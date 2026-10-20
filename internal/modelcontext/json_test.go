package modelcontext

import (
	"encoding/json"
	"testing"
)

func TestStructuredDataTextUndoesHTMLEscapes(t *testing.T) {
	// A literal backslash followed by "u003c" must not be mistaken for an escape.
	literalEscape := "literal \\" + "u003c stays"
	lineSeparators := string([]rune{0x2028, 0x2029})
	raw, err := json.Marshal(map[string]string{
		"html":      "<b>a & b</b>" + lineSeparators,
		"backslash": literalEscape,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := StructuredDataText(raw)
	want := `{"backslash":"literal \\` + "u003c" + ` stays","html":"<b>a & b</b>` + lineSeparators + `"}`
	if got != want {
		t.Fatalf("StructuredDataText = %s, want %s", got, want)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("rendered text is no longer valid JSON: %v", err)
	}
	if decoded["backslash"] != literalEscape {
		t.Fatalf("escaped backslash was rewritten: %q", decoded["backslash"])
	}
}

func TestStructuredDataTextLiftsMultilineFieldsVerbatim(t *testing.T) {
	output := "line \"one\"\n\tpath\\to <x> & y\n"
	raw, err := json.Marshal(struct {
		ProcessID string `json:"process_id"`
		Output    string `json:"output"`
		Done      bool   `json:"done"`
	}{ProcessID: "prc_1", Output: output, Done: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"process_id":"prc_1","done":true}` + "\n\n<output>\n" + output + "</output>"
	if got := StructuredDataText(raw); got != want {
		t.Fatalf("StructuredDataText =\n%s\nwant\n%s", got, want)
	}
}

func TestStructuredDataTextOmitsEmptyMetadata(t *testing.T) {
	raw := json.RawMessage(`{"content":"a\nb"}`)
	if got, want := StructuredDataText(raw), "<content>\na\nb\n</content>"; got != want {
		t.Fatalf("StructuredDataText = %q, want %q", got, want)
	}
}

func TestStructuredDataTextKeepsSingleLineJSON(t *testing.T) {
	raw := json.RawMessage(`{"nested":{"text":"a\nb"},"list":["x"],"s":"one line"}`)
	if got := StructuredDataText(raw); got != string(raw) {
		t.Fatalf("StructuredDataText = %s, want unchanged %s", got, raw)
	}
}
