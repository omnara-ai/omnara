package memorystore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const memoryListingTraversalLimit = 10000

var errFileListFull = errors.New("file listing is full")

func (s *Store) ListFiles(
	ctx context.Context,
	projectID uuid.UUID,
	attachments []agentconfig.MemoryStoreCompiled,
	pattern string,
	matcher *regexp.Regexp,
	limit int,
) (listing.FileListResult, error) {
	rows, access, err := s.listAttachedStores(ctx, projectID, attachments, pattern, limit+1)
	if err != nil {
		return listing.FileListResult{}, err
	}
	remaining := memoryListingTraversalLimit
	view := storeFS{
		globFS: globFS{checkCanceled: ctx.Err, remaining: &remaining},
		files:  s.files, projectID: projectID, stores: rows,
	}
	defer func() { _ = view.Close() }()
	entries := make([]listing.FileEntry, 0, limit+1)
	err = globFiles(ctx, &view, memoryGlobPattern(pattern), &remaining, func(name string, item fs.DirEntry) error {
		full := "/" + name
		if name == "memory" || !matcher.MatchString(full) {
			return nil
		}
		entry := listing.FileEntry{Path: full, Type: listing.FileTypeDirectory}
		if !item.IsDir() {
			info, err := item.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			size := info.Size()
			entry.Type, entry.SizeBytes = listing.FileTypeFile, &size
		} else if store, ok := view.store(strings.TrimPrefix(name, "memory/")); ok {
			mode := access[store.ID]
			if store.ReadOnly {
				mode = agentconfig.MemoryStoreAccessReadOnly
			}
			entry.Description, entry.Access = store.Description, string(mode)
		}
		entries = append(entries, entry)
		if len(entries) > limit {
			return errFileListFull
		}
		return nil
	})
	truncated := errors.Is(err, errFileListFull) || errors.Is(err, errFileTraversalLimit)
	if err != nil && !truncated {
		return listing.FileListResult{}, fmt.Errorf("list memory files: %w", err)
	}
	return listing.FileListResult{Entries: entries[:min(len(entries), limit)], Truncated: truncated}, nil
}

func (s *Store) listAttachedStores(
	ctx context.Context,
	projectID uuid.UUID,
	attachments []agentconfig.MemoryStoreCompiled,
	pattern string,
	limit int,
) ([]dbsqlc.ListAttachedMemoryStoresRow, map[uuid.UUID]agentconfig.MemoryStoreAccess, error) {
	ids := make([]uuid.UUID, 0, len(attachments))
	access := make(map[uuid.UUID]agentconfig.MemoryStoreAccess, len(attachments))
	for _, attached := range attachments {
		ids = append(ids, attached.ID)
		access[attached.ID] = attached.Access
	}
	prefix := pattern
	if i := strings.IndexAny(prefix, "*?"); i >= 0 {
		prefix = prefix[:i]
	}
	params := dbsqlc.ListAttachedMemoryStoresParams{ProjectID: projectID, StoreIds: ids}
	if rest, ok := strings.CutPrefix(prefix, Root+"/"); ok {
		name, _, complete := strings.Cut(rest, "/")
		params.StorePrefix = name
		if complete || prefix == pattern {
			params.StoreName = name
		}
	}
	if limit > 0 && pattern == Root+"/"+params.StorePrefix+"*" {
		rowLimit := int32(limit)
		params.RowLimit = &rowLimit
	}
	rows, err := s.q.ListAttachedMemoryStores(ctx, params)
	return rows, access, err
}

type SearchStore struct {
	Name string
	Root *os.Root
}

func (s *Store) VisitSearchStores(
	ctx context.Context, projectID, agentID uuid.UUID, pattern string,
	visit func(SearchStore) error,
) error {
	attachments, err := s.LoadAgentAttachments(ctx, projectID, agentID)
	if err != nil {
		return err
	}
	storePattern, _, descends := strings.Cut(strings.TrimPrefix(pattern, Root+"/"), "/")
	if !descends && storePattern != "**" {
		return nil
	}
	storePattern = memoryGlobEscaper.Replace(storePattern)
	stores, _, err := s.listAttachedStores(ctx, projectID, attachments, pattern, 0)
	if err != nil {
		return err
	}
	view := storeFS{globFS: globFS{checkCanceled: ctx.Err}, files: s.files, projectID: projectID, stores: stores}
	defer func() { _ = view.Close() }()
	for _, store := range stores {
		matches, err := doublestar.Match(storePattern, store.Name)
		if err != nil {
			return err
		}
		if !matches {
			continue
		}
		root, err := view.openStore(store)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !strings.ContainsAny(pattern, "*?") {
			_, name, err := ParsePath(pattern)
			if err != nil {
				return err
			}
			if err := memoryops.CheckPath(root, name); err != nil {
				return fileReadError(err)
			}
			info, err := root.Stat(name)
			if err != nil {
				return fileReadError(err)
			}
			if !info.Mode().IsRegular() {
				return storeerr.InvalidRequest(errors.New("memory path is not a regular file"))
			}
		}
		if err := visit(SearchStore{Name: store.Name, Root: root}); err != nil {
			return err
		}
	}
	return nil
}
