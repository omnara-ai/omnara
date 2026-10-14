package memorystore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
)

var memoryGlobEscaper = strings.NewReplacer("[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}")

func memoryGlobPattern(pattern string) string {
	escaped := memoryGlobEscaper.Replace(pattern[1:])
	parts := strings.Split(escaped, "/")
	if i := slices.Index(parts, "**"); i >= 0 {
		parts = parts[:i+1]
	}
	return strings.Join(parts, "/")
}

var ErrFileTraversalLimit = errors.New("file listing resource limit reached; narrow the pattern and retry")

type storeFS struct {
	globFS
	files     *Filesystem
	projectID uuid.UUID
	stores    []dbsqlc.ListAttachedMemoryStoresRow
	opened    string
	after     string
}

func (v *storeFS) Close() error {
	if v.root != nil {
		return v.root.Close()
	}
	return nil
}

func (v *storeFS) store(name string) (*dbsqlc.ListAttachedMemoryStoresRow, bool) {
	i, ok := slices.BinarySearchFunc(v.stores, name, func(row dbsqlc.ListAttachedMemoryStoresRow, name string) int {
		return strings.Compare(row.Name, name)
	})
	if !ok {
		return nil, false
	}
	return &v.stores[i], true
}

func (v *storeFS) resolve(name string) (*dbsqlc.ListAttachedMemoryStoresRow, string, error) {
	if !fs.ValidPath(name) {
		return nil, "", fs.ErrInvalid
	}
	rest, ok := strings.CutPrefix(name, "memory/")
	if !ok {
		return nil, "", fs.ErrNotExist
	}
	storeName, relative, _ := strings.Cut(rest, "/")
	store, ok := v.store(storeName)
	if !ok {
		return nil, "", fs.ErrNotExist
	}
	if relative == "" {
		relative = "."
	}
	return store, relative, nil
}

func (v *storeFS) openStore(store dbsqlc.ListAttachedMemoryStoresRow) (*os.Root, error) {
	if err := v.checkCanceled(); err != nil {
		return nil, err
	}
	if v.opened == store.Name {
		return v.root, nil
	}
	if err := v.Close(); err != nil {
		return nil, err
	}
	v.root, v.opened = nil, ""
	ref, err := memoryops.NewStoreRef(store.OrgID, v.projectID, store.ID, store.Name)
	if err != nil {
		return nil, err
	}
	root, err := v.files.OpenStore(ref)
	if err != nil {
		return nil, err
	}
	v.root, v.opened = root, store.Name
	return root, nil
}

func (v *storeFS) Open(name string) (fs.File, error) {
	if err := v.checkCanceled(); err != nil {
		return nil, err
	}
	if name == "." || name == "memory" {
		return &storeDirectory{view: v, path: name, name: name}, nil
	}
	store, relative, err := v.resolve(name)
	if err != nil {
		return nil, err
	}
	if relative == "." {
		return &storeDirectory{view: v, path: name, name: store.Name}, nil
	}
	if _, err := v.openStore(*store); err != nil {
		return nil, err
	}
	return v.globFS.Open(relative)
}

func (v *storeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." && v.finished(name) {
		return nil, nil
	}
	if err := v.checkCanceled(); err != nil {
		return nil, err
	}
	if name == "." {
		return []fs.DirEntry{fs.FileInfoToDirEntry(&storeDirectory{name: "memory"})}, nil
	}
	if name == "memory" {
		entries := make([]fs.DirEntry, 0, len(v.stores))
		for _, store := range v.stores {
			if v.finished(name + "/" + store.Name) {
				continue
			}
			entries = append(entries, fs.FileInfoToDirEntry(&storeDirectory{name: store.Name}))
		}
		return entries, nil
	}
	store, relative, err := v.resolve(name)
	if err != nil {
		return nil, err
	}
	if _, err := v.openStore(*store); err != nil {
		if relative == "." && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := v.globFS.ReadDir(relative)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(entries, func(entry fs.DirEntry) bool {
		return v.finished(name + "/" + entry.Name())
	}), nil
}

func (v *storeFS) finished(name string) bool {
	return compareFilePaths(name, v.after) < 0 && !strings.HasPrefix(v.after, name+"/")
}

func compareFilePaths(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

type storeDirectory struct {
	view    *storeFS
	path    string
	name    string
	entries []fs.DirEntry
	loaded  bool
	offset  int
}

func (d *storeDirectory) Name() string               { return d.name }
func (d *storeDirectory) Size() int64                { return 0 }
func (d *storeDirectory) Mode() fs.FileMode          { return fs.ModeDir | 0500 }
func (d *storeDirectory) ModTime() time.Time         { return time.Time{} }
func (d *storeDirectory) IsDir() bool                { return true }
func (d *storeDirectory) Sys() any                   { return nil }
func (d *storeDirectory) Stat() (fs.FileInfo, error) { return d, nil }
func (d *storeDirectory) Close() error               { return nil }
func (d *storeDirectory) Read([]byte) (int, error)   { return 0, syscall.EISDIR }
func (d *storeDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	if !d.loaded {
		entries, err := d.view.ReadDir(d.path)
		if err != nil {
			return nil, err
		}
		d.entries, d.loaded = entries, true
	}
	if n > 0 && d.offset == len(d.entries) {
		return nil, io.EOF
	}
	end := len(d.entries)
	if n > 0 {
		end = min(end, d.offset+n)
	}
	entries := d.entries[d.offset:end]
	d.offset = end
	return entries, nil
}

type globFS struct {
	root          *os.Root
	checkCanceled func() error
	remaining     *int
}

func (g globFS) Open(name string) (fs.File, error) {
	if err := g.checkCanceled(); err != nil {
		return nil, err
	}
	var file *os.File
	err := memoryops.CheckPath(g.root, name)
	if err == nil {
		file, err = g.root.Open(name)
	}
	if errors.Is(err, syscall.ENOTDIR) {
		err = fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (g globFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := g.checkCanceled(); err != nil {
		return nil, err
	}
	if *g.remaining <= 0 {
		return nil, ErrFileTraversalLimit
	}
	file, err := g.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("memory path is not a directory")
	}
	entries, err := directory.ReadDir(*g.remaining + 1)
	if errors.Is(err, syscall.ENOTDIR) {
		return nil, fs.ErrNotExist
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > *g.remaining {
		return nil, ErrFileTraversalLimit
	}
	*g.remaining -= len(entries)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func globFiles(
	ctx context.Context, fsys fs.FS, pattern string, visit func(string, fs.DirEntry) error,
) error {
	return doublestar.GlobWalk(fsys, pattern, func(name string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return nil
		}
		return visit(name, entry)
	}, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
}
