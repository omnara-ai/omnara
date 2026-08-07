package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/tooloutput"
)

func TestReadArtifactLinesFallsBackToBytePagingForGiantLine(t *testing.T) {
	content := []byte(strings.Repeat("é", tooloutput.ArtifactPageBytes))
	result := readArtifactLines(content, "art_test", 1, 10)
	chunk, ok := result["content"].(string)
	if !ok || len(chunk) > tooloutput.ArtifactPageBytes || !strings.HasPrefix(chunk, "é") {
		t.Fatalf("invalid giant-line chunk: %#v", result)
	}
	next, ok := result["next_offset_byte"].(int)
	if !ok || next != len(chunk) || next <= 0 || next >= len(content) {
		t.Fatalf("next byte offset = %#v", result["next_offset_byte"])
	}
	page, err := readArtifactBytes(content, "art_test", next, tooloutput.ArtifactPageBytes)
	if err != nil {
		t.Fatalf("continue giant line: %v", err)
	}
	if page["offset_byte"] != next {
		t.Fatalf("continued page = %#v", page)
	}
}

func TestSearchArtifactCountsOnlyVisibleMatchesAndCentersExcerpt(t *testing.T) {
	lines := make([]string, 0, 400)
	for range 400 {
		lines = append(lines, strings.Repeat("prefix-", 100)+"NEEDLE"+strings.Repeat("-suffix", 100))
	}
	result := searchArtifactLines(
		[]byte(strings.Join(lines, "\n")),
		"art_test",
		regexp.MustCompile("NEEDLE"),
		tooloutput.SearchArtifactMaxMatches,
		5,
	)
	matches, ok := result["matches"].([]map[string]any)
	if !ok {
		t.Fatalf("matches type = %T", result["matches"])
	}
	if result["match_count"] != len(matches) || len(matches) == 0 || len(matches) >= 100 {
		t.Fatalf("visible match accounting = %#v", result)
	}
	summary, ok := result["summary"].(string)
	if !ok || result["truncated"] != true || !strings.Contains(summary, "at least") {
		t.Fatalf("truncation summary = %#v", result)
	}
	firstLines, ok := matches[0]["lines"].([]map[string]any)
	if !ok || len(firstLines) == 0 {
		t.Fatalf("first match lines = %#v", matches[0]["lines"])
	}
	matchText, ok := firstLines[0]["text"].(string)
	if !ok {
		t.Fatalf("first match text = %#v", firstLines[0]["text"])
	}
	if !strings.Contains(matchText, "NEEDLE") || len(matchText) > tooloutput.SearchArtifactLineBytes+len("……") {
		t.Fatalf("match-centered excerpt = %q", matchText)
	}
}

func TestTruncateAroundMatchKeepsMatchNearEnd(t *testing.T) {
	value := strings.Repeat("a", 2_000) + "TARGET"
	got := truncateAroundMatch(value, 2_000, 2_006, 100)
	if !strings.Contains(got, "TARGET") || len(got) > 103 {
		t.Fatalf("centered truncation = %q", got)
	}
}
