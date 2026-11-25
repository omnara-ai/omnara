package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/testutil/testfs"
)

func TestSearchStoreContents(t *testing.T) {
	setupSandbox(t)
	dir := testfs.TempDir(t)
	for name, content := range map[string]string{
		"a.txt": "target\n", "b.txt": "target\n", "binary.bin": "target\n\x00binary", "empty.txt": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	store := memorystore.SearchStore{Name: "notes", Root: root}
	for _, test := range []struct {
		name, path       string
		limit, matches   int
		searched, binary bool
		wantErr          error
	}{
		{name: "glob skips binary", path: "*", limit: 10, matches: 2, searched: true},
		{name: "exact binary stdin", path: "binary.bin", limit: 10, matches: 1, searched: true, binary: true},
		{name: "only binary glob", path: "*.bin", limit: 10},
		{name: "unmatched glob", path: "missing*", limit: 10},
		{name: "empty exact file is searched", path: "empty.txt", limit: 10, searched: true},
		{name: "shared result limit", path: "*.txt", limit: 1, matches: 1, searched: true, wantErr: errSearchResultLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := searchRequestForTest(t, []string{"-e", "target"}, test.limit)
			input.Path = "/memory/notes/" + test.path
			input.matcher, err = storage.CompileFilePattern(input.Path)
			if err != nil {
				t.Fatal(err)
			}
			output := &searchOutput{input: input}
			searched, err := output.searchStoreContents(t.Context(), store)
			if !errors.Is(err, test.wantErr) || searched != test.searched || output.result.MatchCount != test.matches {
				t.Fatalf("searched=%v matches=%d error=%v", searched, output.result.MatchCount, err)
			}
			if output.result.Truncated != test.binary {
				t.Fatalf("binary truncation=%v, want %v", output.result.Truncated, test.binary)
			}
			for _, line := range output.result.Lines {
				if line.Path != "/memory/notes/a.txt" && line.Path != "/memory/notes/b.txt" &&
					line.Path != "/memory/notes/binary.bin" {
					t.Fatalf("unexpected logical path %q", line.Path)
				}
			}
		})
	}
}
