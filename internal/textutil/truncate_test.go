package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{name: "zero limit returns empty", input: "hello", limit: 0, want: ""},
		{name: "negative limit returns empty", input: "hello", limit: -3, want: ""},
		{name: "empty input", input: "", limit: 5, want: ""},
		{name: "shorter than limit unchanged", input: "hi", limit: 5, want: "hi"},
		{name: "exactly limit unchanged", input: "hello", limit: 5, want: "hello"},
		{name: "ascii truncated", input: "hello world", limit: 5, want: "hello"},
		{name: "multibyte truncated on rune boundary", input: "日本語テスト", limit: 3, want: "日本語"},
		{name: "multibyte exactly limit unchanged", input: "日本語", limit: 3, want: "日本語"},
		{name: "multibyte under limit unchanged", input: "日本語", limit: 10, want: "日本語"},
		{name: "emoji truncated on rune boundary", input: "🚀🔥🎉🌟", limit: 2, want: "🚀🔥"},
		{name: "mixed ascii and multibyte", input: "ab日c", limit: 3, want: "ab日"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateRunes(tc.input, tc.limit)
			if got != tc.want {
				t.Fatalf("TruncateRunes(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateRunes(%q, %d) produced invalid UTF-8: %q", tc.input, tc.limit, got)
			}
			if tc.limit > 0 && utf8.RuneCountInString(got) > tc.limit {
				t.Fatalf("TruncateRunes(%q, %d) returned %d runes, exceeds limit", tc.input, tc.limit, utf8.RuneCountInString(got))
			}
			if !strings.HasPrefix(tc.input, got) {
				t.Fatalf("TruncateRunes(%q, %d) = %q is not a prefix of the input", tc.input, tc.limit, got)
			}
		})
	}
}
