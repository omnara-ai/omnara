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
	for _, pattern := range []string{
		"/memory/notes/*.md", "/memory/notes/?.md", "/memory/*/**/*.md",
		"/memory/**/notes/*.md", "/memory/notes/[x]{y}.md", "/memory/notes/two words.md",
		"/memory/notes/nested/*.md", "/memory/**", "/memory/other/**", "/memory/notes/missing*",
	} {
		t.Run(pattern, func(t *testing.T) {
			matcher, err := storage.CompileFilePattern(pattern)
			if err != nil {
				t.Fatal(err)
			}
			var want, got []string
			for _, name := range names {
				if full := "/memory/notes/" + name; matcher.MatchString(full) {
					want = append(want, full)
				}
			}
			err = store.VisitContents(t.Context(), pattern, matcher, func(path string, content []byte) error {
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
		})
	}
	pattern := "/memory/notes/**"
	matcher, err := storage.CompileFilePattern(pattern)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.VisitContents(ctx, pattern, matcher, func(string, []byte) error {
		t.Fatal("visited file after cancellation")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	stop := errors.New("stop after first file")
	visits := 0
	if err := store.VisitContents(t.Context(), pattern, matcher, func(string, []byte) error {
		visits++
		return stop
	}); !errors.Is(err, stop) || visits != 1 {
		t.Fatalf("visitor stop: visits=%d, err=%v", visits, err)
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
		pattern := "/memory/notes/escape/secret.md"
		matcher, err := storage.CompileFilePattern(pattern)
		if err != nil {
			t.Fatal(err)
		}
		err = store.VisitContents(t.Context(), pattern, matcher, func(string, []byte) error {
			t.Fatal("read through explicit symlink")
			return nil
		})
		if !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("explicit symlink: %v", err)
		}
	})
}
