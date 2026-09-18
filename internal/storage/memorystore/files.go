package memorystore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
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

func (s *Store) Read(ctx context.Context, scope Scope, storeID uuid.UUID, path string) (string, []byte, error) {
	if err := ValidatePath(path); err != nil {
		return "", nil, storeerr.InvalidRequest(err)
	}
	ref, err := s.authorizeFile(ctx, s.q, scope, storeID, false)
	if err != nil {
		return "", nil, err
	}
	root, err := s.files.OpenStore(ref)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, storeerr.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	if _, err := s.authorizeFile(ctx, s.q, scope, storeID, false); err != nil {
		return "", nil, err
	}
	body, err := memoryops.Read(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, storeerr.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return blobstore.ContentDigest(body), body, nil
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
	if write && store.ReadOnly {
		return memoryops.StoreRef{}, fmt.Errorf("memory store is read-only: %w", storeerr.ErrConflict)
	}
	return memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, id, store.Name)
}

func (s *Store) Write(ctx context.Context, input WriteInput) (string, error) {
	if err := ValidatePath(input.Path); err != nil {
		return "", storeerr.InvalidRequest(err)
	}
	if len(input.Content) > daemonprotocol.MaxFileTransferBytes {
		return "", storeerr.InvalidRequest(errors.New("memory content must be at most 10 MiB"))
	}
	if input.ExpectedDigest != nil {
		if err := daemonprotocol.ValidateFileDigest(*input.ExpectedDigest); err != nil {
			return "", storeerr.InvalidRequest(err)
		}
	}
	ref, err := s.authorizeFile(ctx, s.q, input.Scope, input.StoreID, true)
	if err != nil {
		return "", err
	}
	staged, err := s.files.Stage(ref, input.Content)
	if err != nil {
		return "", fmt.Errorf("stage memory file: %w", err)
	}
	defer s.files.Discard(staged)
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.Scope.OrgID, input.Scope.ProjectID); err != nil {
		return "", err
	}
	q := s.q.WithTx(tx)
	if _, err := s.authorizeFile(ctx, q, input.Scope, input.StoreID, true); err != nil {
		return "", err
	}
	root, err := s.files.OpenStore(ref)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var current []byte
	if root != nil {
		defer func() { _ = root.Close() }()
		current, err = memoryops.Read(root, input.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	digest := blobstore.ContentDigest(input.Content)
	if err == nil {
		currentDigest := blobstore.ContentDigest(current)
		if currentDigest == digest {
			return digest, s.files.Sync(ref, input.Path)
		}
		if input.ExpectedDigest == nil {
			return "", fmt.Errorf("expected_digest is required to change an existing file: %w", storeerr.ErrConflict)
		}
		if *input.ExpectedDigest != currentDigest {
			return "", fmt.Errorf("memory changed; download it and retry: %w", storeerr.ErrConflict)
		}
	} else {
		if input.ExpectedDigest != nil {
			return "", fmt.Errorf("memory file does not exist: %w", storeerr.ErrConflict)
		}
		limits, err := resourceguard.ResolveLimits(ctx, q, input.Scope.OrgID)
		if err != nil {
			return "", err
		}
		if limits.MaxMemoriesPerStore <= 0 {
			return "", fmt.Errorf("memory file limit reached: %w", storeerr.ErrConflict)
		}
		if root != nil {
			var count int64
			if err := fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
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
			}); err != nil {
				return "", err
			}
		}
	}
	if err := s.files.Publish(ctx, ref, root, input.Path, staged); err != nil {
		return "", err
	}
	return digest, nil
}
