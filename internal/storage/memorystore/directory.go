package memorystore

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DirectoryEntry struct {
	Path       string
	Directory  bool
	Size       int64
	ModifiedAt time.Time
}

type DirectoryPage struct {
	Entries []DirectoryEntry
	Next    string
}

func (s *Store) ListDirectory(
	ctx context.Context, scope Scope, storeID uuid.UUID, dir, after string, limit int,
) (DirectoryPage, error) {
	if dir != "" {
		if err := ValidatePath(dir); err != nil {
			return DirectoryPage{}, storeerr.InvalidRequest(err)
		}
	}
	if limit < 1 || limit > 100 {
		return DirectoryPage{}, storeerr.InvalidRequest(errors.New("invalid limit"))
	}
	root, err := s.openStoreForRead(ctx, scope, storeID)
	if errors.Is(err, fs.ErrNotExist) && dir == "" {
		return DirectoryPage{}, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return DirectoryPage{}, storeerr.ErrNotFound
	}
	if err != nil {
		return DirectoryPage{}, err
	}
	defer func() { _ = root.Close() }()
	name := dir
	if name == "" {
		name = "."
	}
	if err := memoryops.CheckPath(root, name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DirectoryPage{}, storeerr.ErrNotFound
		}
		return DirectoryPage{}, err
	}
	entries, err := fs.ReadDir(root.FS(), name)
	if errors.Is(err, syscall.ENOTDIR) {
		return DirectoryPage{}, storeerr.InvalidRequest(errors.New("path must identify a directory"))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return DirectoryPage{}, storeerr.ErrNotFound
	}
	if err != nil {
		return DirectoryPage{}, err
	}
	page := DirectoryPage{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return DirectoryPage{}, err
		}
		if entry.Name() <= after || (!entry.IsDir() && !entry.Type().IsRegular()) {
			continue
		}
		if len(page.Entries) == limit {
			page.Next = path.Base(page.Entries[len(page.Entries)-1].Path)
			break
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return DirectoryPage{}, err
		}
		page.Entries = append(page.Entries, DirectoryEntry{
			Path: path.Join(dir, entry.Name()), Directory: info.IsDir(), Size: info.Size(), ModifiedAt: info.ModTime(),
		})
	}
	return page, nil
}
