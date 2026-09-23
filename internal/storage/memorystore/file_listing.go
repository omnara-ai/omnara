package memorystore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	entries := make([]listing.FileEntry, 0, limit+1)
	truncated := false
	parts := strings.Split(pattern[1:], "/")
	recursive := slices.Contains(parts, "**")
	var rowLimit *int32
	if len(parts) == 2 && !recursive {
		value := int32(limit + 1)
		rowLimit = &value
	}
	rows, access, queryErr := s.listAttachedStores(ctx, projectID, attachments, pattern, matcher.String(), rowLimit)
	if queryErr != nil {
		return listing.FileListResult{}, queryErr
	}
	remaining := memoryListingTraversalLimit
	for _, row := range rows {
		root := Root + "/" + row.Name
		if matcher.MatchString(root) {
			mode := access[row.ID]
			if row.ReadOnly {
				mode = agentconfig.MemoryStoreAccessReadOnly
			}
			entries = append(entries, listing.FileEntry{
				Path: root, Type: listing.FileTypeDirectory, Description: row.Description, Access: string(mode),
			})
		}
		if len(entries) > limit {
			break
		}
		filePattern := memoryFilePattern(pattern, row.Name)
		if filePattern == "" {
			continue
		}
		visit := func(name string, item fs.DirEntry) error {
			full := root + "/" + name
			if !matcher.MatchString(full) {
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
			}
			entries = append(entries, entry)
			if len(entries) > limit {
				return errFileListFull
			}
			return nil
		}
		err := s.withStoreRoot(ctx, projectID, row, func(root *os.Root) error {
			return globFiles(ctx, root, filePattern, &remaining, visit)
		})
		if errors.Is(err, errFileListFull) || errors.Is(err, errFileTraversalLimit) {
			truncated = true
			break
		}
		if err != nil {
			return listing.FileListResult{}, fmt.Errorf("list memory files: %w", err)
		}
	}
	if len(entries) > limit {
		truncated = true
		entries = entries[:limit]
	}
	return listing.FileListResult{Entries: entries, Truncated: truncated}, nil
}

func (s *Store) withStoreRoot(
	ctx context.Context, projectID uuid.UUID, store dbsqlc.ListAttachedMemoryStoresRow, visit func(*os.Root) error,
) error {
	ref, err := memoryops.NewStoreRef(store.OrgID, projectID, store.Name)
	if err != nil {
		return err
	}
	root, err := s.files.OpenStore(ref)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := s.q.GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{
		ProjectID: projectID, ID: store.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return visit(root)
}

func (s *Store) listAttachedStores(
	ctx context.Context,
	projectID uuid.UUID,
	attachments []agentconfig.MemoryStoreCompiled,
	pattern, rootPattern string,
	limit *int32,
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
	if limit != nil {
		params.RootPattern, params.RowLimit = rootPattern, limit
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
	stores, _, err := s.listAttachedStores(ctx, projectID, attachments, pattern, "", nil)
	if err != nil {
		return err
	}
	for _, store := range stores {
		if memoryFilePattern(pattern, store.Name) == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err = s.withStoreRoot(ctx, projectID, store, func(root *os.Root) error {
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
			return visit(SearchStore{Name: store.Name, Root: root})
		})
		if err != nil {
			return err
		}
	}
	return nil
}
