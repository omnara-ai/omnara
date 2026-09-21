package storage

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MemorySearchStore struct {
	Name string
	Root *os.Root
}

func (s *Store) VisitMemorySearchStores(
	ctx context.Context, projectID, agentID uuid.UUID, pattern string,
	visit func(MemorySearchStore) error,
) error {
	matcher, err := CompileFilePattern(pattern)
	if err != nil {
		return err
	}
	raw, err := dbsqlc.New(s.pool).GetAgentMemoryConfig(ctx, dbsqlc.GetAgentMemoryConfigParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if err != nil {
		return err
	}
	stores, _, err := s.memoryListingStores(ctx, projectID, raw, pattern, matcher, nil)
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
		err = s.withMemoryStoreRoot(ctx, projectID, store, func(root *os.Root) error {
			if !strings.ContainsAny(pattern, "*?") {
				_, name, err := memorystore.ParsePath(pattern)
				if err != nil {
					return err
				}
				if err := memoryops.CheckPath(root, name); errors.Is(err, os.ErrNotExist) {
					return storeerr.ErrNotFound
				} else if err != nil {
					return err
				}
				info, err := root.Stat(name)
				if errors.Is(err, os.ErrNotExist) {
					return storeerr.ErrNotFound
				}
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return storeerr.InvalidRequest(errors.New("memory path is not a regular file"))
				}
			}
			return visit(MemorySearchStore{Name: store.Name, Root: root})
		})
		if err != nil {
			return err
		}
	}
	return nil
}
