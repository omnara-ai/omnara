package memorystore

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/testfs"
)

func TestWindowsMemoryPaths(t *testing.T) {
	for _, name := range []string{
		"NUL", "con.txt", "nested/AUX", "COM1.md", "CON .txt", "COM¹.ext", "con.a.b", "file:stream",
		"dir./file", "dir /file", "file.", "file ", "<file", "file>", `file"`, "file|",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePath(name); err == nil {
				t.Fatal("accepted an unsupported Windows filename")
			}
		})
	}
	for _, name := range []string{
		"notes.md", "NOTES.md", "nested/é😀.txt", "[x]{y}.md", "two words.md",
		"literal~1.txt", ".hidden", " .txt", "notes.a.b",
	} {
		if err := ValidatePath(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWindowsMemoryGlobPaths(t *testing.T) {
	root, err := os.OpenRoot(testfs.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	remaining := 100
	filesystem := globFS{root: root, checkCanceled: t.Context().Err, remaining: &remaining}
	for _, pattern := range []string{"a:b/*", "con.txt/**"} {
		err := globFiles(t.Context(), filesystem, pattern, func(string, fs.DirEntry) error {
			t.Fatal("visited an unsupported Windows filename")
			return nil
		})
		if !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("glob %s: got %v, want invalid request", pattern, err)
		}
	}
}
