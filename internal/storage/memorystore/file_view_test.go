package memorystore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
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
			filesystem := globFS{root: root, checkCanceled: t.Context().Err, remaining: &remaining}
			err := globFiles(t.Context(), filesystem, test.pattern, &remaining, func(name string, _ fs.DirEntry) error {
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
		t.Context(), filesystem, "dir/deep/note.md", &remaining, func(string, fs.DirEntry) error { return nil },
	); err != nil {
		t.Fatalf("literal path walked tree: %v", err)
	}
	remaining = 2
	if err := globFiles(
		t.Context(), filesystem, "**/*.absent", &remaining, func(string, fs.DirEntry) error {
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
		err := globFiles(t.Context(), filesystem, pattern, &remaining, func(string, fs.DirEntry) error { return nil })
		if !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("symlink prefix %s: %v", pattern, err)
		}
	}
	remaining = 100
	err = globFiles(t.Context(), filesystem, "**", &remaining, func(name string, _ fs.DirEntry) error {
		if strings.HasPrefix(name, "link") || strings.HasPrefix(name, "escape") {
			t.Fatalf("followed symlink %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreFilesystem(t *testing.T) {
	files, err := memoryops.OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	remaining := 10000
	orgID, projectID := uuid.New(), uuid.New()
	view := storeFS{
		globFS: globFS{checkCanceled: t.Context().Err, remaining: &remaining},
		files:  files, projectID: projectID,
	}
	defer func() { _ = view.Close() }()
	var expected []string
	for _, name := range []string{"a", "b", "empty"} {
		store := dbsqlc.ListAttachedMemoryStoresRow{ID: uuid.New(), OrgID: orgID, Name: name}
		view.stores = append(view.stores, store)
		if name == "empty" {
			continue
		}
		ref, err := memoryops.NewStoreRef(orgID, projectID, store.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"note.md", "nested/note.md"} {
			staged, err := files.Stage(ref, []byte(name))
			if err != nil {
				t.Fatal(err)
			}
			if err := files.Publish(t.Context(), ref, nil, path, staged); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, "memory/"+name+"/"+path)
		}
	}
	entries, err := fs.ReadDir(&view, "memory")
	if err != nil || len(entries) != 3 || view.root != nil || remaining != 10000 {
		t.Fatalf("store listing opened files: %v %v", entries, err)
	}
	if _, err := fs.Stat(&view, "memory/a"); err != nil || view.root != nil {
		t.Fatalf("store metadata opened files: %v", err)
	}
	if err := fstest.TestFS(&view, expected...); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"/memory/*/**/*.md", "/**/**/*.md", "/**/*/*.md"} {
		remaining = 100
		var paths []string
		visit := func(name string, item fs.DirEntry) error {
			if !item.IsDir() {
				paths = append(paths, name)
			}
			return nil
		}
		err := globFiles(t.Context(), &view, memoryGlobPattern(pattern), &remaining, visit)
		slices.Sort(paths)
		slices.Sort(expected)
		if err != nil || !slices.Equal(paths, expected) || remaining != 94 {
			t.Fatalf("pattern %s: paths=%v remaining=%d err=%v", pattern, paths, remaining, err)
		}
	}
	root, err := view.openStore(view.stores[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(t.TempDir(), "escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadDir(&view, "memory/a/escape"); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("followed symlink: %v", err)
	}
	if _, err := view.Open("memory/unattached/note.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opened unattached store: %v", err)
	}
}
