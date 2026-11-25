package memorystore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/testfs"
)

func TestSearchContents(t *testing.T) {
	dir := testfs.TempDir(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	names := []string{"note.md", ".hidden.md", "[x]{y}.md", "two words.md", "é.md", "nested/note.md", "nested/other.txt"}
	for _, name := range names {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := memorystore.SearchStore{Name: "notes", Root: root}
	for _, test := range []struct {
		pattern string
		want    []string
	}{
		{"/memory", nil},
		{"/memory/*", nil},
		{"/memory/notes", nil},
		{"/memory/notes/*.md", []string{"note.md", ".hidden.md", "[x]{y}.md", "two words.md", "é.md"}},
		{"/memory/notes/?.md", []string{"é.md"}},
		{"/memory/*/**/*.md", []string{"note.md", ".hidden.md", "[x]{y}.md", "two words.md", "é.md", "nested/note.md"}},
		{"/memory/**/notes/*.md", []string{"note.md", ".hidden.md", "[x]{y}.md", "two words.md", "é.md"}},
		{"/memory/notes/[x]{y}.md", []string{"[x]{y}.md"}},
		{"/memory/notes/two words.md", []string{"two words.md"}},
		{"/memory/notes/nested/*.md", []string{"nested/note.md"}},
		{"/memory/no*/nested/**", []string{"nested/note.md", "nested/other.txt"}},
		{"/memory/**", names},
		{"/memory/other/**", nil},
		{"/memory/notes/missing*", nil},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			matcher, err := storage.CompileFilePattern(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			var want, got []string
			for _, name := range test.want {
				want = append(want, "/memory/notes/"+name)
			}
			err = store.VisitContents(t.Context(), test.pattern, matcher, func(path string, content []byte) error {
				got = append(got, path)
				if path != "/memory/notes/"+string(content) {
					t.Fatalf("wrong file content: %q = %q", path, content)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(want, got) {
				t.Fatalf("paths = %v, want %v", got, want)
			}
			if _, err := root.Stat("note.md"); err != nil {
				t.Fatalf("borrowed root was closed: %v", err)
			}
		})
	}
	pattern := "/memory/notes/**"
	matcher, err := storage.CompileFilePattern(pattern)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, pattern := range []string{pattern, "/memory/other/**", "/memory"} {
		matcher, err := storage.CompileFilePattern(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.VisitContents(ctx, pattern, matcher, func(string, []byte) error {
			t.Fatal("visited file after cancellation")
			return nil
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation for %s: %v", pattern, err)
		}
	}
	stop := errors.New("stop after first file")
	visits := 0
	if err := store.VisitContents(t.Context(), pattern, matcher, func(string, []byte) error {
		visits++
		return stop
	}); !errors.Is(err, stop) || visits != 1 {
		t.Fatalf("visitor stop: visits=%d, err=%v", visits, err)
	}
	if _, err := root.Stat("note.md"); err != nil {
		t.Fatalf("borrowed root was closed after visitor error: %v", err)
	}
	t.Run("symlinks", func(t *testing.T) {
		outside := testfs.TempDir(t)
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := root.Symlink(outside, "escape"); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Remove("escape") }()
		if err := store.VisitContents(t.Context(), pattern, matcher, func(path string, content []byte) error {
			if string(content) == "outside" {
				t.Fatalf("read outside root through %q", path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, storeName := range []string{"notes", "other"} {
			pattern := "/memory/" + storeName + "/escape/secret.md"
			matcher, err := storage.CompileFilePattern(pattern)
			if err != nil {
				t.Fatal(err)
			}
			err = store.VisitContents(t.Context(), pattern, matcher, func(string, []byte) error {
				t.Fatal("read through explicit symlink")
				return nil
			})
			var want error
			if storeName == "notes" {
				want = storeerr.ErrConflict
			}
			if !errors.Is(err, want) {
				t.Fatalf("explicit symlink for %s: %v, want %v", storeName, err, want)
			}
		}
	})
}
