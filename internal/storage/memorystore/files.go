package memorystore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
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

func OpenUnpreparedFilesystem(dir string) (*Filesystem, error) {
	return memoryops.OpenUnpreparedFilesystem(dir)
}

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

type FileView struct {
	Digest string
	At     time.Time
}

func (s *Store) LastSeen(ctx context.Context, agentID uuid.UUID, path string) (FileView, bool, error) {
	row, err := s.q.GetAgentMemoryFileLastSeen(ctx, dbsqlc.GetAgentMemoryFileLastSeenParams{
		AgentID: agentID, Path: path,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FileView{}, false, nil
	}
	if err != nil {
		return FileView{}, false, fmt.Errorf("load memory file history: %w", err)
	}
	return FileView{Digest: row.Digest, At: row.CompletedAt}, true, nil
}

type File struct {
	Digest     string
	Content    []byte
	ModifiedAt time.Time
}

func (s *Store) Read(ctx context.Context, scope Scope, storeID uuid.UUID, path string) (string, []byte, error) {
	file, err := s.ReadFile(ctx, scope, storeID, path)
	return file.Digest, file.Content, err
}

func (s *Store) ReadFile(ctx context.Context, scope Scope, storeID uuid.UUID, path string) (File, error) {
	if err := ValidatePath(path); err != nil {
		return File{}, storeerr.InvalidRequest(err)
	}
	root, err := s.openStoreForRead(ctx, scope, storeID)
	if err != nil {
		return File{}, fileReadError(err)
	}
	defer func() { _ = root.Close() }()
	body, modified, err := memoryops.ReadModified(root, path)
	if err != nil {
		return File{}, fileReadError(err)
	}
	return File{Digest: blobstore.ContentDigest(body), Content: body, ModifiedAt: modified}, nil
}

func fileReadError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return storeerr.Tag(storeerr.ErrNotFound, storeerr.ErrPathNotFound)
	}
	if errors.Is(err, storeerr.ErrConflict) || errors.Is(err, syscall.ENOTDIR) {
		return storeerr.InvalidRequest(err)
	}
	return err
}

func (s *Store) openStoreForRead(ctx context.Context, scope Scope, storeID uuid.UUID) (*os.Root, error) {
	ref, err := s.authorizeFile(ctx, s.q, scope, storeID, false)
	if err != nil {
		return nil, err
	}
	return s.files.OpenStore(ref)
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
		return memoryops.StoreRef{}, fmt.Errorf("memory store is unavailable: %w", mapped(err))
	}
	if write && scope.AgentID != uuid.Nil && store.AgentAccess != string(agentconfig.MemoryStoreAccessReadWrite) {
		return memoryops.StoreRef{}, storeerr.Tag(storeerr.ErrFileReadOnly, storeerr.Tag(storeerr.ErrConflict, errors.New(
			"memory store is read-only; choose a writable store or request write access",
		)))
	}
	return memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, store.ID, store.Name)
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
			logent.MemoryStagedFileDiscardFailed(ctx, input.Scope.OrgID, input.Scope.ProjectID, input.StoreID, err)
		}
	}()
	result := WriteResult{Path: Root + "/" + ref.Name + "/" + input.Path, Digest: blobstore.ContentDigest(input.Content)}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.Scope.OrgID, input.Scope.ProjectID); err != nil {
		return WriteResult{}, err
	}
	q := s.q.WithTx(tx)
	fileCount, err := q.LockMemoryStoreShared(ctx, dbsqlc.LockMemoryStoreSharedParams{
		ProjectID: input.Scope.ProjectID, ID: input.StoreID,
	})
	if err != nil {
		return WriteResult{}, fmt.Errorf("memory store is unavailable: %w", mapped(err))
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
	var count int64
	if exists {
		if currentDigest == result.Digest {
			return result, s.files.Sync(ref, root, input.Path)
		}
		if input.ExpectedDigest == nil {
			return WriteResult{}, storeerr.Tag(storeerr.ErrExpectedDigestRequired, storeerr.Tag(
				&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
				errors.New("expected_digest is required to change an existing file; read the file and retry with its digest"),
			))
		}
		if *input.ExpectedDigest != currentDigest {
			return WriteResult{}, storeerr.Tag(
				&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
				errors.New("memory changed; retrieve the latest contents and retry"),
			)
		}
	} else {
		if input.ExpectedDigest != nil {
			return WriteResult{}, storeerr.Tag(&storeerr.FileContentConflictError{}, errors.New("memory file does not exist"))
		}
		if count, err = checkFileCapacity(ctx, q, input.Scope.OrgID, root, fileCount); err != nil {
			return WriteResult{}, err
		}
	}
	if input.Scope.AgentID != uuid.Nil {
		if _, err := q.LockAgentForMemoryWrite(ctx, dbsqlc.LockAgentForMemoryWriteParams{
			ProjectID: input.Scope.ProjectID, AgentID: input.Scope.AgentID,
		}); err != nil {
			return WriteResult{}, fmt.Errorf("lock agent for memory write: %w", mapped(err))
		}
		if err := s.authorizeAttachment(ctx, q, input.Scope, input.StoreID, true); err != nil {
			return WriteResult{}, err
		}
	}
	if err := s.files.Publish(ctx, ref, root, input.Path, staged); err != nil {
		return WriteResult{}, err
	}
	if !exists {
		_ = tx.Rollback(ctx)
		countCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.q.SetMemoryStoreFileCount(countCtx, dbsqlc.SetMemoryStoreFileCountParams{
			FileCount: count + 1, ProjectID: input.Scope.ProjectID, ID: input.StoreID,
		})
	}
	return result, nil
}

func checkFileCapacity(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID uuid.UUID,
	root *os.Root,
	fileCount *int64,
) (int64, error) {
	limits, err := resourceguard.ResolveLimits(ctx, q, orgID)
	if err != nil {
		return 0, err
	}
	var count int64
	if fileCount != nil {
		count = *fileCount
	} else if root != nil {
		if count, err = countFiles(ctx, root); err != nil {
			return 0, err
		}
	}
	if count >= limits.MaxMemoriesPerStore {
		return 0, fmt.Errorf("memory file limit reached: %w", storeerr.ErrConflict)
	}
	return count, nil
}

func countFiles(ctx context.Context, root *os.Root) (int64, error) {
	var count int64
	err := fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	return count, err
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
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, scope.OrgID, scope.ProjectID); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	fileCount, err := q.LockMemoryStoreShared(ctx, dbsqlc.LockMemoryStoreSharedParams{
		ProjectID: scope.ProjectID, ID: storeID,
	})
	if err != nil {
		return fmt.Errorf("memory store is unavailable: %w", mapped(err))
	}
	if _, err := s.authorizeFile(ctx, q, scope, storeID, true); err != nil {
		return err
	}
	root, err := s.files.OpenStore(ref)
	if errors.Is(err, fs.ErrNotExist) {
		return fileReadError(err)
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	currentDigest, err := memoryops.Digest(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileReadError(err)
	}
	if err != nil {
		return err
	}
	if currentDigest != expectedDigest {
		return storeerr.Tag(
			&storeerr.FileContentConflictError{CurrentDigest: currentDigest},
			errors.New("memory changed; retrieve the latest contents and retry"),
		)
	}
	if err := s.files.RemoveFile(ref, root, path); err != nil {
		return err
	}
	if fileCount != nil {
		_ = tx.Rollback(ctx)
		countCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.q.SetMemoryStoreFileCount(countCtx, dbsqlc.SetMemoryStoreFileCountParams{
			FileCount: max(*fileCount-1, 0), ProjectID: scope.ProjectID, ID: storeID,
		})
	}
	return nil
}
