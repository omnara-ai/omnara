package memorystore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Filesystem = memoryops.Filesystem

func OpenFilesystem(dir string) (*Filesystem, error) { return memoryops.OpenFilesystem(dir) }

type WriteInput struct {
	Scope          Scope
	StoreID        uuid.UUID
	Path           string
	Content        []byte
	ExpectedDigest *string
}

type WriteResult struct {
	Path   string
	Digest string
}

func (s *Store) Read(ctx context.Context, scope Scope, storeID uuid.UUID, path string) (string, []byte, error) {
	if err := ValidatePath(path); err != nil {
		return "", nil, storeerr.InvalidRequest(err)
	}
	root, err := s.openStoreForRead(ctx, scope, storeID)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, storeerr.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	body, err := memoryops.Read(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, storeerr.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return blobstore.ContentDigest(body), body, nil
}

func (s *Store) openStoreForRead(ctx context.Context, scope Scope, storeID uuid.UUID) (*os.Root, error) {
	ref, err := s.authorizeFile(ctx, s.q, scope, storeID, false)
	if err != nil {
		return nil, err
	}
	root, err := s.files.OpenStore(ref)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizeFile(ctx, s.q, scope, storeID, false); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func (s *Store) authorizeFile(
	ctx context.Context,
	q *dbsqlc.Queries,
	scope Scope,
	id uuid.UUID,
	write bool,
) (memoryops.StoreRef, error) {
	if err := s.authorizeAttachment(ctx, q, scope, id, write); err != nil {
		return memoryops.StoreRef{}, err
	}
	store, err := q.GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return memoryops.StoreRef{}, mapped(err)
	}
	if write && scope.AgentID != uuid.Nil && store.ReadOnly {
		return memoryops.StoreRef{}, fmt.Errorf("memory store is read-only: %w", storeerr.ErrConflict)
	}
	return memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, store.Name)
}

func (s *Store) Write(ctx context.Context, input WriteInput) (WriteResult, error) {
	if err := ValidatePath(input.Path); err != nil {
		return WriteResult{}, storeerr.InvalidRequest(err)
	}
	if len(input.Content) > daemonprotocol.MaxFileTransferBytes {
		return WriteResult{}, storeerr.InvalidRequest(errors.New("memory content must be at most 10 MiB"))
	}
	if input.ExpectedDigest != nil {
		if err := daemonprotocol.ValidateFileDigest(*input.ExpectedDigest); err != nil {
			return WriteResult{}, storeerr.InvalidRequest(err)
		}
	}
	ref, err := s.authorizeFile(ctx, s.q, input.Scope, input.StoreID, true)
	if err != nil {
		return WriteResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	staged, err := s.files.Stage(ref, input.Content)
	if err != nil {
		return WriteResult{}, fmt.Errorf("stage memory file: %w", err)
	}
	defer func() {
		if err := s.files.Discard(staged); err != nil {
			logent.MemoryCleanupFailed(ctx, logent.MemoryCleanupDiscardStagedFile, input.Scope.OrgID, input.Scope.ProjectID, input.StoreID, err)
		}
	}()
	result := WriteResult{Path: Root + "/" + ref.Name + "/" + input.Path, Digest: blobstore.ContentDigest(input.Content)}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.Scope.OrgID, input.Scope.ProjectID); err != nil {
		return WriteResult{}, err
	}
	q := s.q.WithTx(tx)
	if _, err := q.LockMemoryStoreShared(ctx, dbsqlc.LockMemoryStoreSharedParams{
		ProjectID: input.Scope.ProjectID, ID: input.StoreID,
	}); err != nil {
		return WriteResult{}, mapped(err)
	}
	if _, err := s.authorizeFile(ctx, q, input.Scope, input.StoreID, true); err != nil {
		return WriteResult{}, err
	}
	root, err := s.files.OpenStore(ref)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return WriteResult{}, err
	}
	var currentDigest string
	exists := false
	if root != nil {
		defer func() { _ = root.Close() }()
		currentDigest, err = memoryops.Digest(root, input.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return WriteResult{}, err
		}
		exists = err == nil
	}
	if exists {
		if currentDigest == result.Digest {
			return result, s.files.Sync(ref, root, input.Path)
		}
		if input.ExpectedDigest == nil {
			return WriteResult{}, fmt.Errorf(
				"expected_digest is required to change an existing file: %w",
				&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
			)
		}
		if *input.ExpectedDigest != currentDigest {
			return WriteResult{}, fmt.Errorf(
				"memory changed; download it and retry: %w",
				&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
			)
		}
	} else {
		if input.ExpectedDigest != nil {
			return WriteResult{}, fmt.Errorf("memory file does not exist: %w", &storeerr.FileContentConflictError{})
		}
		if err := checkFileCapacity(ctx, q, input.Scope.OrgID, root); err != nil {
			return WriteResult{}, err
		}
	}
	if err := s.files.Publish(ctx, ref, root, input.Path, staged); err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

func checkFileCapacity(ctx context.Context, q *dbsqlc.Queries, orgID uuid.UUID, root *os.Root) error {
	limits, err := resourceguard.ResolveLimits(ctx, q, orgID)
	if err != nil {
		return err
	}
	if limits.MaxMemoriesPerStore <= 0 {
		return fmt.Errorf("memory file limit reached: %w", storeerr.ErrConflict)
	}
	if root == nil {
		return nil
	}
	var count int64
	return fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		if count >= limits.MaxMemoriesPerStore {
			return fmt.Errorf("memory file limit reached: %w", storeerr.ErrConflict)
		}
		return nil
	})
}

func (s *Store) DeleteFile(ctx context.Context, scope Scope, storeID uuid.UUID, path, expectedDigest string) error {
	if err := ValidatePath(path); err != nil {
		return storeerr.InvalidRequest(err)
	}
	if err := daemonprotocol.ValidateFileDigest(expectedDigest); err != nil {
		return storeerr.InvalidRequest(err)
	}
	ref, err := s.authorizeFile(ctx, s.q, scope, storeID, true)
	if err != nil {
		return err
	}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, scope.OrgID, scope.ProjectID); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	if _, err := q.LockMemoryStoreShared(ctx, dbsqlc.LockMemoryStoreSharedParams{
		ProjectID: scope.ProjectID, ID: storeID,
	}); err != nil {
		return mapped(err)
	}
	if _, err := s.authorizeFile(ctx, q, scope, storeID, true); err != nil {
		return err
	}
	root, err := s.files.OpenStore(ref)
	if errors.Is(err, fs.ErrNotExist) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	currentDigest, err := memoryops.Digest(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentDigest != expectedDigest {
		return fmt.Errorf(
			"memory changed; reload it and retry: %w",
			&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
		)
	}
	return s.files.RemoveFile(ref, root, path)
}
