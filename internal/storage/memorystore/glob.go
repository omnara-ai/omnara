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

	"github.com/bmatcuk/doublestar/v4"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
)

var memoryGlobEscaper = strings.NewReplacer("[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}")

func memoryFilePattern(pattern, store string) string {
	escaped := memoryGlobEscaper.Replace(pattern[1:])
	parts := strings.Split(escaped, "/")
	if i := slices.Index(parts, "**"); i >= 0 {
		parts = parts[:i+1]
	}
	for _, component := range []string{"memory", store} {
		if len(parts) == 0 {
			return ""
		}
		if parts[0] == "**" {
			break
		}
		if match, _ := doublestar.Match(parts[0], component); !match {
			return ""
		}
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

var errFileTraversalLimit = errors.New("memory listing traversal limit reached")

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
	return file, err
}

func (g globFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := g.checkCanceled(); err != nil {
		return nil, err
	}
	if *g.remaining <= 0 {
		return nil, errFileTraversalLimit
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
		entries = entries[:*g.remaining]
		*g.remaining = -1
	} else {
		*g.remaining -= len(entries)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func globFiles(
	ctx context.Context, root *os.Root, pattern string, remaining *int, visit func(string, fs.DirEntry) error,
) error {
	fsys := globFS{root: root, checkCanceled: ctx.Err, remaining: remaining}
	err := doublestar.GlobWalk(fsys, pattern, func(name string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return nil
		}
		return visit(name, entry)
	}, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
	if err != nil {
		return err
	}
	if *remaining < 0 {
		return errFileTraversalLimit
	}
	return nil
}
