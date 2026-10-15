package filesearch

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func search(t *testing.T, args []string, limit, offset int, text string) (Result, error) {
	t.Helper()
	options, err := ParseArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := Compile(options)
	if err != nil {
		t.Fatal(err)
	}
	collector := &Collector{Mode: options.Mode, Limit: limit, OffsetLine: offset}
	err = collector.Search("notes.txt", []byte(text), matcher, options)
	return collector.Result, err
}

func TestSearchMatchesRipgrepSemantics(t *testing.T) {
	for _, test := range []struct {
		name, text string
		args       []string
		lines      []int
		count      uint64
		files      bool
	}{
		{"regex", "deploy\nDEPLOY\nundeployed\n", []string{"-e", "deploy"}, []int{1, 3}, 0, false},
		{"literal", "foo(bar)\nfoobar\n", []string{"-F", "-e", "foo(bar)"}, []int{1}, 0, false},
		{"case", "deploy\nDEPLOY\n", []string{"-i", "-e", "deploy"}, []int{1, 2}, 0, false},
		{"word", "deploy\nundeployed\n", []string{"-w", "-e", "deploy"}, []int{1}, 0, false},
		{"line", "deploy now\ndeploy\n", []string{"-x", "-e", "deploy"}, []int{2}, 0, false},
		{"invert", "# heading\nbody\n", []string{"-v", "-e", "^#"}, []int{2}, 0, false},
		{"multiple", "deploy\nrollback\n", []string{"-e", "deploy", "-e", "rollback"}, []int{1, 2}, 0, false},
		{"multiline", "start\nend\nno\nstart\nend\n", []string{"-U", "-e", "start\\nend"}, []int{1, 4}, 0, false},
		{"files", "deploy\n", []string{"-l", "-e", "deploy"}, nil, 0, true},
		{"counts", strings.Repeat("deploy deploy\n", 100), []string{"-c", "-e", "deploy"}, nil, 100, true},
		{"empty", "", []string{"-e", "^$"}, nil, 0, false},
		{"no phantom line", "line\n", []string{"-e", "^$"}, nil, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := search(t, test.args, 20, 1, test.text)
			if err != nil {
				t.Fatal(err)
			}
			var lines []int
			for _, line := range result.Lines {
				if line.IsMatch {
					lines = append(lines, line.LineNumber)
				}
			}
			if !slices.Equal(lines, test.lines) {
				t.Fatalf("lines = %v, want %v", lines, test.lines)
			}
			if test.files && (len(result.Files) != 1 || result.Files[0].Path != "notes.txt") {
				t.Fatalf("files = %+v", result.Files)
			}
			if test.count != 0 && (result.Files[0].Count == nil || *result.Files[0].Count != test.count) {
				t.Fatalf("count = %+v", result.Files)
			}
		})
	}
}

func TestSearchContextAndLimits(t *testing.T) {
	text := "before\nTARGET\nTARGET\nafter\nTARGET\nlast\n"
	for _, test := range []struct {
		name          string
		args          []string
		limit, offset int
		lines         []int
		matches       int
		limited       bool
	}{
		{"context", []string{"-C", "2", "-e", "TARGET"}, 20, 1, []int{1, 2, 3, 4, 5, 6}, 3, false},
		{"limit", []string{"-C", "2", "-e", "TARGET"}, 1, 1, []int{1, 2}, 1, true},
		{"context before next match", []string{"-C", "2", "-e", "TARGET"}, 2, 1, []int{1, 2, 3, 4}, 2, true},
		{"offset", []string{"-C", "2", "-e", "TARGET"}, 20, 3, []int{3, 4, 5, 6}, 2, false},
		{"after", []string{"-A", "1", "-e", "TARGET"}, 20, 1, []int{2, 3, 4, 5, 6}, 3, false},
		{"before", []string{"-B", "1", "-e", "after"}, 20, 1, []int{3, 4}, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := search(t, test.args, test.limit, test.offset, text)
			if test.limited != errors.Is(err, ErrResultLimit) || !test.limited && err != nil {
				t.Fatalf("search error: %v", err)
			}
			var lines []int
			for _, line := range result.Lines {
				lines = append(lines, line.LineNumber)
				if line.Text != strings.Split(text, "\n")[line.LineNumber-1] {
					t.Fatalf("incorrect text: %+v", line)
				}
			}
			if !slices.Equal(lines, test.lines) || result.MatchCount != test.matches {
				t.Fatalf("incorrect results: %+v", result)
			}
		})
	}
}

func TestSearchSnippetsAndMultilineBlocks(t *testing.T) {
	result, err := search(t, []string{"-U", "-e", "TARGET\\nEND"}, 20, 1, strings.Repeat("x", 5000)+"TARGET\nEND\nnext\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 1 {
		t.Fatalf("lines = %+v", result.Lines)
	}
	line := result.Lines[0]
	if line.LineNumber != 1 || line.EndLine != 2 || len(line.Text) > LineBytes ||
		!strings.HasPrefix(line.Text, "…") || !strings.Contains(line.Text, "TARGET\nEND") {
		t.Fatalf("multiline match = %+v", line)
	}
	result, err = search(t, []string{"-e", "TARGET"}, 20, 1, "TARGET\xff\n")
	if err != nil || len(result.Lines) != 1 || result.Lines[0].Text != "TARGET�" {
		t.Fatalf("invalid UTF-8 snippet = %+v, %v", result.Lines, err)
	}
}

func TestFinishReportsTruncation(t *testing.T) {
	collector := &Collector{Limit: 1}
	result, err := collector.Finish(ErrResultLimit)
	if err != nil || !result.Truncated || result.IncompleteReason != "search result limit reached; narrow the search" {
		t.Fatalf("finish = %+v, %v", result, err)
	}
	if _, err := (&Collector{}).Finish(errors.New("boom")); err == nil {
		t.Fatal("finish hid a search failure")
	}
}
