package memorystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
)

const (
	stagedFileGrace                  = time.Hour
	maximumReportedFileRemovalErrors = 10
)

type FileCleanupResult struct {
	RemovedStores        int
	DiscardedStagedFiles int
}

func (s *Store) CleanupFiles(ctx context.Context) (FileCleanupResult, error) {
	defer s.recorder.StartCleanup().ObserveDuration()
	removed, removeErr := s.removePendingStoreFiles(ctx)
	discarded, discardErr := s.discardStaleStagedFiles(ctx)
	return FileCleanupResult{RemovedStores: removed, DiscardedStagedFiles: discarded},
		errors.Join(removeErr, discardErr)
}

func (s *Store) removePendingStoreFiles(ctx context.Context) (int, error) {
	stores, err := s.q.ListMemoryStoresPendingFileRemoval(ctx)
	if err != nil {
		return 0, fmt.Errorf("list memory stores pending file removal: %w", err)
	}
	if len(stores) == 0 {
		return 0, nil
	}
	if err := s.files.CheckPrepared(); err != nil {
		return 0, err
	}
	removed := 0
	omitted := 0
	var errs []error
	for _, store := range stores {
		if ctx.Err() != nil {
			break
		}
		err := s.removeStoreFiles(ctx, store)
		switch {
		case err == nil:
			removed++
		case len(errs) < maximumReportedFileRemovalErrors:
			errs = append(errs, err)
		default:
			omitted++
		}
	}
	if omitted > 0 {
		errs = append(errs, fmt.Errorf("%d additional memory file removal errors omitted", omitted))
	}
	return removed, errors.Join(errs...)
}

func (s *Store) removeStoreFiles(ctx context.Context, store dbsqlc.ListMemoryStoresPendingFileRemovalRow) error {
	if err := s.removeStoreFolders(store); err != nil {
		return err
	}
	err := s.q.MarkMemoryStoreFilesRemoved(ctx, dbsqlc.MarkMemoryStoreFilesRemovedParams{
		ProjectID: store.ProjectID, ID: store.ID,
	})
	if err != nil {
		return fmt.Errorf("mark memory store files removed: %w", err)
	}
	return nil
}

func (s *Store) removeStoreFolders(store dbsqlc.ListMemoryStoresPendingFileRemovalRow) error {
	if store.OrgDeleted {
		return s.files.RemoveScope(store.OrgID, nil)
	}
	if store.ProjectDeleted {
		return s.files.RemoveScope(store.OrgID, &store.ProjectID)
	}
	ref, err := memoryops.NewStoreRef(store.OrgID, store.ProjectID, store.ID, store.Name)
	if err != nil {
		return err
	}
	return s.files.RemoveStore(ref)
}

func (s *Store) discardStaleStagedFiles(ctx context.Context) (int, error) {
	now, err := s.q.DBNow(ctx)
	if err != nil {
		return 0, fmt.Errorf("load database time: %w", err)
	}
	return s.files.DiscardStagedBefore(ctx, now.Add(-stagedFileGrace))
}
