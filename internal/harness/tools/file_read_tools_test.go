package tools

import (
	"encoding/json"
	"regexp"
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
		{name: "giant line", content: "first\n" + strings.Repeat("é", 5000) + "\nlast"},
		{name: "escaped text", content: strings.Repeat("\t", 9000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := test.content
			first := readFileLines([]byte(content), "/artifacts/example", 1, 200)
			got, ok := first["content"].(string)
			if !ok {
				t.Fatal("missing content")
			}
			assertRetrievalBudget(t, first)
			offset := len(got)
			for offset < len(content) {
				page, err := readFileBytes([]byte(content), "/artifacts/example", offset, toolcatalog.ArtifactPageBytes)
				if err != nil {
					t.Fatal(err)
				}
				chunk, ok := page["content"].(string)
				if !ok {
					t.Fatal("missing content")
				}
				if chunk == "" || !utf8.ValidString(chunk) {
					t.Fatal("invalid or stalled page")
				}
				got += chunk
				offset += len(chunk)
				if page["has_more"] == true && page["next_offset_byte"] != offset {
					t.Fatal("incorrect continuation")
				}
				assertRetrievalBudget(t, page)
			}
			if got != content {
				t.Fatal("paging lost content")
			}
		})
	}
	if _, err := readFileBytes([]byte("é"), "path", 0, 1); err == nil {
		t.Fatal("tiny read must report insufficient byte budget")
	}
	if _, err := readFileBytes([]byte("é"), "path", 1, 4); err == nil {
		t.Fatal("accepted interior UTF-8 offset")
	}
	page := readFileLines([]byte("a\nb\nc"), "path", 2, 1)
	if page["content"] != "b\n" || page["next_offset_line"] != 3 {
		t.Fatalf("line page = %v", page)
	}
}

func TestSearchFilesBoundsAndLocations(t *testing.T) {
	text := "before\n" + strings.Repeat("é", 5000) + "TARGET" +
		strings.Repeat("x", 5000) + "\nafter\nTARGET twice TARGET\n"
	result := searchFilesLines([]byte(text), "path", regexp.MustCompile("TARGET"), 1, 1, 1)
	if result["truncated"] != true || result["match_count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	matches, ok := result["matches"].([]map[string]any)
	if !ok || len(matches) != 1 {
		t.Fatal("missing matches")
	}
	if matches[0]["match_line"] != 2 {
		t.Fatal("incorrect match location")
	}
	lines, ok := matches[0]["lines"].([]map[string]any)
	if !ok || len(lines) != 3 {
		t.Fatal("missing context")
	}
	matchText, ok := lines[1]["text"].(string)
	if !ok {
		t.Fatal("missing match text")
	}
	if lines[0]["text"] != "before" || lines[2]["text"] != "after" ||
		!strings.Contains(matchText, "TARGET") {
		t.Fatalf("context = %v", lines)
	}
	assertRetrievalBudget(t, result)
	result = searchFilesLines([]byte(strings.Repeat("\t\t\t\n", 10000)), "path", regexp.MustCompile(`	`), 1, 100, 5)
	assertRetrievalBudget(t, result)
	fullContext := strings.Repeat(strings.Repeat("x", 500)+"\n", 5) + "TARGET\n" +
		strings.Repeat(strings.Repeat("x", 500)+"\n", 5)
	result = searchFilesLines([]byte(fullContext), "path", regexp.MustCompile("TARGET"), 1, 20, 5)
	if result["match_count"] != 1 {
		t.Fatal("context budget hid the first match")
	}
	assertRetrievalBudget(t, result)
	result = searchFilesLines([]byte("line\n"), "path", regexp.MustCompile("^$"), 1, 20, 0)
	if result["match_count"] != 0 {
		t.Fatal("matched phantom trailing line")
	}
}

func TestSearchFilesContinuation(t *testing.T) {
	lines := make([]string, 25)
	for index := range lines {
		lines[index] = strings.Repeat("é", 200)
	}
	matchLines := []int{6, 7, 9, 15}
	for _, line := range matchLines {
		lines[line-1] = "TARGET" + lines[line-1]
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	pattern := regexp.MustCompile("TARGET")
	for _, test := range []struct {
		name                     string
		maxMatches, contextLines int
	}{
		{"match limit", 1, 1},
		{"text budget", 100, 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			offset := 1
			for index, expected := range matchLines {
				result := searchFilesLines(content, "path", pattern, offset, test.maxMatches, test.contextLines)
				matches, ok := result["matches"].([]map[string]any)
				if !ok || len(matches) != 1 || matches[0]["match_line"] != expected {
					t.Fatalf("offset %d: matches = %v", offset, result["matches"])
				}
				context, ok := matches[0]["lines"].([]map[string]any)
				if !ok || len(context) != 2*test.contextLines+1 || context[0]["line_number"] != expected-test.contextLines {
					t.Fatalf("offset %d: context = %v", offset, matches[0]["lines"])
				}
				assertRetrievalBudget(t, result)
				if index == len(matchLines)-1 {
					if result["truncated"] != false || result["next_offset_line"] != nil {
						t.Fatalf("final page has continuation: %v", result)
					}
					break
				}
				next, ok := result["next_offset_line"].(int)
				if !ok || next != matchLines[index+1] || result["truncated"] != true {
					t.Fatalf("offset %d: continuation = %v", offset, result)
				}
				offset = next
			}
		})
	}
	for _, offset := range []int{16, 26, 100} {
		result := searchFilesLines(content, "path", pattern, offset, 20, 1)
		if result["match_count"] != 0 || result["truncated"] != false || result["next_offset_line"] != nil {
			t.Fatalf("offset %d: unexpected matches or continuation: %v", offset, result)
		}
	}
}

func TestSearchFilesUnicodeSnippets(t *testing.T) {
	contextLine := "xx" + strings.Repeat("€", 5000)
	content := contextLine + "\nTARGET" + strings.Repeat("€", 5000) + "\n" + contextLine
	result := searchFilesLines([]byte(content), "path", regexp.MustCompile("TARGET€+"), 1, 20, 1)
	matches, ok := result["matches"].([]map[string]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("matches = %v", result["matches"])
	}
	lines, ok := matches[0]["lines"].([]map[string]any)
	if !ok || len(lines) != 3 {
		t.Fatalf("lines = %v", matches[0]["lines"])
	}
	for index, expected := range []string{
		"xx" + strings.Repeat("€", 84),
		"TARGET" + strings.Repeat("€", 83),
		"xx" + strings.Repeat("€", 84),
	} {
		if lines[index]["text"] != expected {
			t.Fatalf("line %d text = %q, want %q", index, lines[index]["text"], expected)
		}
	}
	assertRetrievalBudget(t, result)
}

func TestFileRetrievalRejectsInvalidInputs(t *testing.T) {
	id, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	path := "/artifacts/" + id
	for _, input := range []map[string]any{
		{"path": "/artifacts"}, {"path": path + "/"}, {"path": "/skills/test"},
		{"path": path, "offset_line": 1, "offset_byte": 0}, {"path": path, "limit_bytes": 0},
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
	for _, pattern := range []string{"", "[", strings.Repeat("x", 1025)} {
		raw, err := json.Marshal(map[string]any{"path": path, "pattern": pattern})
		if err != nil {
			t.Fatal(err)
		}
		if validateSearchFilesInput(raw) == nil {
			t.Fatal("accepted invalid pattern")
		}
	}
	for _, offset := range []any{nil, 1, 3, 0, -1} {
		input := map[string]any{"path": path, "pattern": "TARGET"}
		if offset != nil {
			input["offset_line"] = offset
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		resolved, _, _, err := resolveSearchFilesRequest(raw)
		if offset == 0 || offset == -1 {
			if err == nil {
				t.Fatalf("accepted offset_line = %v", offset)
			}
		} else if err != nil || resolved.OffsetLine == nil ||
			(offset == nil && *resolved.OffsetLine != 1) || (offset != nil && *resolved.OffsetLine != offset) {
			t.Fatalf("offset_line = %v: resolved %v, error %v", offset, resolved.OffsetLine, err)
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
