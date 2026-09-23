package memorystore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestGlobFilesystem(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	names := []string{
		".hidden.md", "root.md", "dir/deep/note.md", "dir/a.txt", "[x]{y}.md", "noise/a", "noise/b", "noise/a.txt/note.txt",
	}
	for _, name := range names {
		full := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		pattern string
		want    []string
	}{
		{"*.md", []string{".hidden.md", "[x]{y}.md", "root.md"}},
		{"**/*.md", []string{".hidden.md", "[x]{y}.md", "dir/deep/note.md", "root.md"}},
		{"dir/*", []string{"dir/a.txt", "dir/deep"}},
		{"dir/deep", []string{"dir/deep"}},
		{"dir/deep/note.md", []string{"dir/deep/note.md"}},
		{`\[x\]\{y\}.md`, []string{"[x]{y}.md"}},
		{"missing/**", nil},
		{"root.md/*", nil},
		{"root.md/**", nil},
		{"dir/a.txt/x", nil},
		{"dir/a.txt/*", nil},
		{"*/a.txt/*", []string{"noise/a.txt/note.txt"}},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			remaining := 100
			var got []string
			err := globFiles(t.Context(), root, test.pattern, &remaining, func(name string, _ fs.DirEntry) error {
				got = append(got, name)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, test.want) {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
	remaining := 0
	filesystem := globFS{root: root, checkCanceled: t.Context().Err, remaining: &remaining}
	for _, name := range []string{"missing", "root.md/child"} {
		file, err := filesystem.Open(name)
		if file != nil || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("open %s: file=%v error=%v", name, file, err)
		}
	}
	if err := globFiles(
		t.Context(), root, "dir/deep/note.md", &remaining, func(string, fs.DirEntry) error { return nil },
	); err != nil {
		t.Fatalf("literal path walked tree: %v", err)
	}
	remaining = 2
	if err := globFiles(
		t.Context(), root, "**/*.absent", &remaining, func(string, fs.DirEntry) error {
			t.Fatal("unexpected match")
			return nil
		},
	); !errors.Is(err, errFileTraversalLimit) {
		t.Fatalf("nonmatching traversal did not stop: %v", err)
	}
	if err := os.Symlink("dir", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"link/deep/*", "escape/**"} {
		remaining = 100
		err := globFiles(t.Context(), root, pattern, &remaining, func(string, fs.DirEntry) error { return nil })
		if !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("symlink prefix %s: %v", pattern, err)
		}
	}
	remaining = 100
	err = globFiles(t.Context(), root, "**", &remaining, func(name string, _ fs.DirEntry) error {
		if strings.HasPrefix(name, "link") || strings.HasPrefix(name, "escape") {
			t.Fatalf("followed symlink %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMemoryGlobProjectionCandidates(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"root.md", "team/inner.md", "team/team/deep.md"} {
		full := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, pattern := range []string{
		"/memory/team/**/*.md", "/**/team/**/*.md", "/**/**/*.md", "/**/*/*.md",
		"/memory/team/**/team/**/*.md", "/memory/team/**/team/**/team/**/*.md",
	} {
		t.Run(pattern, func(t *testing.T) {
			want := []string{"root.md", "team", "team/inner.md", "team/team", "team/team/deep.md"}
			remaining := 20
			var got []string
			walkPattern := memoryFilePattern(pattern, "team")
			err = globFiles(t.Context(), root, walkPattern, &remaining, func(name string, _ fs.DirEntry) error {
				got = append(got, name)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
			if strings.HasPrefix(pattern, "/**") && remaining < 15 {
				t.Fatalf("repeated traversal: visited %d entries for a tree with 5 entries", 20-remaining)
			}
		})
	}
}
