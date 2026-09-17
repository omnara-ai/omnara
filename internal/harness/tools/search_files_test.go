package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func searchRequestForTest(t *testing.T, args []string, limit int) searchFilesRequest {
	t.Helper()
	data, err := json.Marshal(map[string]any{"path": "/memory/team/file.txt", "args": args, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	input, err := resolveSearchFilesRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestSearchFilesModes(t *testing.T) {
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
			input := searchRequestForTest(t, test.args, 20)
			page := (&searchOutput{input: input})
			if err := page.search(t.Context(), searchSource{path: input.Path, content: []byte(test.text)}); err != nil {
				t.Fatal(err)
			}
			var lines []int
			for _, match := range page.result.Lines {
				if match.IsMatch {
					lines = append(lines, match.LineNumber)
				}
			}
			if !reflect.DeepEqual(lines, test.lines) {
				t.Fatalf("lines = %v, want %v", lines, test.lines)
			}
			if test.files && (len(page.result.Files) != 1 || page.result.Files[0].Path != input.Path) {
				t.Fatalf("files = %+v", page.result.Files)
			}
			if test.count != 0 && (page.result.Files[0].Count == nil || *page.result.Files[0].Count != test.count) {
				t.Fatalf("count = %+v", page.result.Files)
			}
		})
	}
}

func TestSearchFilesContextAndLimits(t *testing.T) {
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
		{"offset", []string{"-C", "2", "-e", "TARGET"}, 20, 3, []int{3, 4, 5, 6}, 2, false},
		{"after", []string{"-A", "1", "-e", "TARGET"}, 20, 1, []int{2, 3, 4, 5, 6}, 3, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := searchRequestForTest(t, test.args, test.limit)
			input.OffsetLine = &test.offset
			output := &searchOutput{input: input}
			err := output.search(t.Context(), searchSource{path: input.Path, content: []byte(text)})
			if test.limited && !errors.Is(err, errSearchResultLimit) || !test.limited && err != nil {
				t.Fatalf("search error: %v", err)
			}
			var lines []int
			for _, line := range output.result.Lines {
				lines = append(lines, line.LineNumber)
				if line.Text != strings.Split(text, "\n")[line.LineNumber-1] {
					t.Fatalf("incorrect text: %+v", line)
				}
			}
			if !slices.Equal(lines, test.lines) || output.result.MatchCount != test.matches {
				t.Fatalf("incorrect results: %+v", output.result)
			}
		})
	}
}

func TestSearchFilesResponseBudget(t *testing.T) {
	input := searchRequestForTest(t, []string{"-C", "5", "-e", "TARGET"}, 100)
	input.Path = "/memory/team/" + strings.Repeat("name/", 190) + "file.txt"
	output := &searchOutput{input: input}
	err := output.search(t.Context(), searchSource{path: input.Path,
		content: []byte(strings.Repeat("before\nTARGET\nafter\n", 20))})
	if !errors.Is(err, errSearchResultLimit) || output.used > toolcatalog.FilePageBytes || len(output.result.Lines) == 0 {
		t.Fatalf("response was not bounded: %+v, %v", output, err)
	}
}

func TestSearchFilesTextAndOutputBounds(t *testing.T) {
	input := searchRequestForTest(t, []string{"-C", "5", "-e", "TARGET"}, 100)
	text := strings.Repeat(strings.Repeat("é", 5000)+"\n", 5) + "TARGET" +
		strings.Repeat("x", 5000) + "\n" + strings.Repeat("after\n", 5)
	page := (&searchOutput{input: input})
	if err := page.search(t.Context(), searchSource{path: input.Path, content: []byte(text)}); err != nil {
		t.Fatal(err)
	}
	if page.result.MatchCount != 1 || page.used > toolcatalog.FilePageBytes {
		t.Fatalf("missing or oversized match: %+v", page.result)
	}
	for _, line := range page.result.Lines {
		if !utf8.ValidString(line.Text) {
			t.Fatal("invalid UTF-8 snippet")
		}
	}
	input = searchRequestForTest(t, []string{"-e", "["}, 20)
	if (&searchOutput{input: input}).search(t.Context(), searchSource{}) == nil {
		t.Fatal("accepted invalid regex")
	}
}

func TestSearchFilesNativeBinaryContent(t *testing.T) {
	for _, test := range []struct {
		name, content, snippet string
		binary                 bool
	}{
		{"binary", "TARGET\n\x00binary", "TARGET", true},
		{"late binary", "TARGET\n" + strings.Repeat("x\n", 40000) + "\x00binary", "TARGET", true},
		{"invalid UTF-8", "TARGET\xff\n", "TARGET�", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := searchRequestForTest(t, []string{"-e", "TARGET"}, 20)
			page := &searchOutput{input: input}
			if err := page.search(t.Context(), searchSource{path: input.Path, content: []byte(test.content)}); err != nil {
				t.Fatal(err)
			}
			if page.result.MatchCount != 1 || len(page.result.Lines) != 1 || page.result.Lines[0].Text != test.snippet ||
				page.result.Truncated != test.binary || test.binary && page.result.IncompleteReason == "" {
				t.Fatalf("incorrect native content result: %+v", page.result)
			}
		})
	}
}

func TestSearchFilesRejectsUnsafeArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-e", "x", "/etc/passwd"}, {"--pre", "cat", "-e", "x"}, {"-f", "/etc/passwd"},
		{"--follow", "-e", "x"}, {"-P", "-e", "x"}, {"-m", "1", "-e", "x"},
		{"--json", "-e", "x"}, {"-e"}, {"-C", "6", "-e", "x"}, {"-l", "-c", "-e", "x"},
		{"-e", strings.Repeat("x", 1025)}, {"-e", ""}, {"-e", "a\x00b"},
	} {
		if _, err := parseSearchArgs(args); err == nil {
			t.Fatalf("accepted unsafe args: %q", args)
		}
	}
	for _, data := range []string{
		`{"path":"/memory/team/../private/*","args":["-e","x"]}`,
		`{"path":"/artifacts/*.txt","args":["-e","x"]}`,
		`{"path":"/memory/**/*.md","args":["-e","x"],"offset_line":2}`,
		`{"path":"/memory/team/file","args":["-c","-e","x"],"offset_line":2}`,
		`{"path":"/memory/team/file","args":["-e","x"],"cursor":"bad"}`,
		`{"path":"/memory/team/file","pattern":"x"}`,
	} {
		if validateSearchFilesInput(json.RawMessage(data)) == nil {
			t.Fatalf("accepted invalid input: %s", data)
		}
	}
}

func TestSearchFilesResourceLimits(t *testing.T) {
	input := searchRequestForTest(t, []string{"-e", "TARGET"}, 20)
	page := &searchOutput{input: input}
	err := page.search(t.Context(), searchSource{content: []byte(strings.Repeat("x", 3*1024*1024) + "TARGET")})
	if !errors.Is(err, errSearchOutputLimit) {
		t.Fatalf("oversized result was not bounded: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := page.search(ctx, searchSource{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestMemorySearchGlobs(t *testing.T) {
	root := t.TempDir()
	names := []string{"a.md", "é.md", "nested/a.md", "team/a.md", "[x]{y}.md", ".hidden.md", "a:b.md", "trailing ", "two words.md"}
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, pattern := range []string{
		"/memory/team/*.md", "/memory/team/?.md", "/memory/*/**/*.md", "/memory/**/team/*.md",
		"/memory/**/**/team/*.md", "/memory/team/[x]{y}.md", "/memory/team/trailing ",
		"/memory/team/two words.md", "/memory/team/a**.md",
	} {
		t.Run(pattern, func(t *testing.T) {
			matcher, err := storage.CompileFilePattern(pattern)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, name := range names {
				if matcher.MatchString("/memory/team/" + name) {
					want = append(want, name)
				}
			}
			args := []string{"--files", "--hidden", "--no-ignore"}
			for _, glob := range memorySearchGlobs(pattern, "team") {
				args = append(args, "--glob", glob)
			}
			command := exec.CommandContext(t.Context(), "rg", append(args, "--", ".")...)
			command.Dir = root
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, name := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
				name = strings.TrimPrefix(name, "./")
				if matcher.MatchString("/memory/team/" + name) {
					got = append(got, name)
				}
			}
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
