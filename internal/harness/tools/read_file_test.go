package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestReadFilePagingPreservesContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "unicode lines", content: strings.Repeat("é😀\n", 2000)},
		{name: "giant line", content: "é😀first\n" + strings.Repeat("é😀", 5000) + "\nlast"},
		{name: "escaped text", content: strings.Repeat("\t", 9000)},
		{name: "empty file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte(test.content)
			page := readFileLines(content, "/artifacts/example", 1, toolcatalog.ReadFileDefaultLines)
			var got string
			for {
				chunk, ok := page["content"].(string)
				if !ok || !utf8.ValidString(chunk) || (chunk == "" && page["has_more"] == true) {
					t.Fatal("invalid or stalled page")
				}
				if len(chunk) > toolcatalog.FilePageBytes || page["bytes_read"] != len(chunk) {
					t.Fatalf("invalid byte count: %v", page)
				}
				got += chunk
				if len(got) > len(content) {
					t.Fatal("paging duplicated content")
				}
				assertRetrievalBudget(t, page)
				if page["has_more"] == false {
					if page["next_offset_char"] != nil || page["next_offset_line"] != nil {
						t.Fatal("final page has continuation")
					}
					break
				}
				if offset, ok := page["next_offset_line"].(int); ok {
					page = readFileLines(content, "/artifacts/example", offset, toolcatalog.ReadFileDefaultLines)
				} else if offset, ok := page["next_offset_char"].(int); ok {
					if offset != utf8.RuneCountInString(got) {
						t.Fatalf("character continuation = %d, want %d", offset, utf8.RuneCountInString(got))
					}
					page = readFileChars(content, "/artifacts/example", offset, toolcatalog.ReadFileDefaultChars)
				} else {
					t.Fatal("missing continuation")
				}
			}
			if got != test.content {
				t.Fatal("paging lost content")
			}
		})
	}
	page := readFileLines([]byte("a\nb\nc"), "path", 2, 1)
	if page["content"] != "b\n" || page["next_offset_line"] != 3 {
		t.Fatalf("line page = %v", page)
	}
}

func TestReadFileChars(t *testing.T) {
	for _, test := range []struct {
		name          string
		content       string
		offset, limit int
		want          string
		wantOffset    int
		nextOffset    any
	}{
		{name: "four-byte character", content: "abc😀z", offset: 3, limit: 1, want: "😀", wantOffset: 3, nextOffset: 4},
		{name: "after multibyte characters", content: "é😀z", offset: 2, limit: 1, want: "z", wantOffset: 2},
		{name: "code point", content: "e\u0301x", offset: 1, limit: 1, want: "\u0301", wantOffset: 1, nextOffset: 2},
		{name: "at EOF", content: "é😀", offset: 2, limit: 1, wantOffset: 2},
		{name: "past EOF", content: "é😀", offset: 100, limit: 1, wantOffset: 2},
		{name: "empty file", offset: 100, limit: 1},
		{
			name: "byte cap", content: strings.Repeat("a", 4095) + "😀z",
			limit: toolcatalog.ReadFileMaxChars, want: strings.Repeat("a", 4095), nextOffset: 4095,
		},
		{
			name: "unicode byte cap", content: strings.Repeat("😀", 2000),
			limit: toolcatalog.ReadFileMaxChars, want: strings.Repeat("😀", 1024), nextOffset: 1024,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := readFileChars([]byte(test.content), "path", test.offset, test.limit)
			if page["content"] != test.want || page["offset_char"] != test.wantOffset ||
				page["bytes_read"] != len(test.want) || page["next_offset_char"] != test.nextOffset ||
				page["has_more"] != (test.nextOffset != nil) {
				t.Fatalf("page = %v", page)
			}
			assertRetrievalBudget(t, page)
		})
	}
}

func TestReadFileDefaults(t *testing.T) {
	for _, input := range []string{
		`{"path":"/memory/team/notes.md"}`,
		`{"path":"/memory/team/notes.md","offset_char":0}`,
		`{"path":"/memory/team/notes.md","limit_chars":512}`,
	} {
		request, err := resolveReadFileRequest(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if request.OffsetChar != nil {
			if *request.OffsetChar != 0 || *request.LimitChars != 512 {
				t.Fatalf("incorrect character defaults: %+v", request)
			}
		} else if *request.OffsetLine != 1 || *request.LimitLines != 100 {
			t.Fatalf("incorrect line defaults: %+v", request)
		}
	}
}

func TestFileRetrievalRejectsInvalidInputs(t *testing.T) {
	id, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	path := "/artifacts/" + id
	for _, input := range []map[string]any{
		{"path": "/artifacts"}, {"path": path + "/"}, {"path": "/skills/test"},
		{"path": path, "offset_line": 1, "offset_char": 0}, {"path": path, "limit_chars": 0},
		{"path": path, "offset_char": -1}, {"path": path, "limit_chars": 4097},
		{"path": path, "offset_byte": 0}, {"path": path, "limit_bytes": 4},
		{"path": "/memory/team"}, {"path": "/memory/team/../other/file"},
		{"path": "/memory/team/file", "limit_lines": 1, "limit_chars": 4},
		{"path": path, "limit_lines": 201}, {"path": path, "unexpected": true},
	} {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if validateReadFileInput(raw) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}

}

func assertRetrievalBudget(t *testing.T, result map[string]any) {
	t.Helper()
	content, err := structuredToolResultContent(result)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := content.contentParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) > 32768 {
		t.Fatalf("retrieval response exceeds compaction budget: %d", len(parts))
	}
}
