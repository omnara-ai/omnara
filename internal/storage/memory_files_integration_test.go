//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestMemoryFileMutationsWithSingleConnection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store, files := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Pool Admin", "admin")
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: testOrgID, CreatedByUserID: admin.ID, Name: "Memory pool key", OrgRole: "admin",
	})
	require.NoError(t, err)
	for _, principal := range []identitystore.PrincipalRecord{
		userPrincipal(admin.ID), identitystore.NewOrgAPIKeyPrincipal(testOrgID, key.Record.ID),
	} {
		t.Run(principal.Type, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			config := pool.Config()
			config.MaxConns, config.MinConns, config.MinIdleConns = 1, 0, 0
			limited, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			t.Cleanup(limited.Close)
			memories := newIntegrationStore(limited, WithMemoryFilesystem(files)).Memories()
			scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: principal}
			resource, err := memories.Create(ctx, scope, strings.ReplaceAll(principal.Type, "_", "-"), "", false)
			require.NoError(t, err)
			input := memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: "notes.txt", Content: []byte("original"),
			}
			created, err := memories.Write(ctx, input)
			require.NoError(t, err)
			input.Content, input.ExpectedDigest = []byte("replacement"), &created.Digest
			updated, err := memories.Write(ctx, input)
			require.NoError(t, err)
			require.NoError(t, memories.DeleteFile(ctx, scope, resource.ID, input.Path, updated.Digest))
		})
	}
}

func TestMemoryStoreNameReuse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	files, err := memoryops.OpenFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Files Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	for _, unfinishedCleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("name-reuse-%t", unfinishedCleanup), func(t *testing.T) {
			t.Parallel()
			original, err := store.Memories().Create(ctx, scope, fmt.Sprintf("reuse-%t", unfinishedCleanup), "", false)
			if err != nil {
				t.Fatal(err)
			}
			oldInput := memorystore.WriteInput{
				Scope: scope, StoreID: original.ID, Path: "old.md", Content: []byte("old content"),
			}
			if _, err := store.Memories().Write(ctx, oldInput); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Memories().Create(ctx, scope, original.Name, "", false); !errors.Is(err, storeerr.ErrConflict) {
				t.Fatalf("duplicate creation: %v", err)
			}
			_, body, err := store.Memories().Read(ctx, scope, original.ID, oldInput.Path)
			if err != nil || !bytes.Equal(body, oldInput.Content) {
				t.Fatalf("duplicate creation damaged content: %q %v", body, err)
			}
			if unfinishedCleanup {
				if err := dbsqlc.New(pool).DeleteMemoryStore(ctx, dbsqlc.DeleteMemoryStoreParams{
					ProjectID: scope.ProjectID, ID: original.ID,
				}); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Memories().Delete(ctx, scope, original.ID); err != nil {
				t.Fatal(err)
			}
			replacement, err := store.Memories().Create(ctx, scope, original.Name, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if replacement.ID == original.ID {
				t.Fatal("reused database identity")
			}
			oldPath := filepath.Join(
				dir,
				mustPublicID(t, publicid.KindOrganization, scope.OrgID),
				mustPublicID(t, publicid.KindProject, scope.ProjectID),
				original.Name, oldInput.Path,
			)
			if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement inherited old file: %v", err)
			}
			if _, err := store.Memories().Write(ctx, oldInput); !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("deleted identity could write: %v", err)
			}
			newInput := memorystore.WriteInput{
				Scope: scope, StoreID: replacement.ID, Path: "new.md", Content: []byte("new content"),
			}
			if _, err := store.Memories().Write(ctx, newInput); err != nil {
				t.Fatal(err)
			}
			if err := store.Memories().Delete(ctx, scope, original.ID); !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("old deletion retry: %v", err)
			}
			_, body, err = store.Memories().Read(ctx, scope, replacement.ID, newInput.Path)
			if err != nil || !bytes.Equal(body, newInput.Content) {
				t.Fatalf("old deletion touched replacement: %q %v", body, err)
			}
		})
	}
}
func memoryFilesystemRef(t *testing.T, scope memorystore.Scope, record memorystore.Record) memoryops.StoreRef {
	t.Helper()
	ref, err := memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, record.Name)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustPublicID(t *testing.T, kind publicid.Kind, id uuid.UUID) string {
	t.Helper()
	value, err := publicid.Encode(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMemoryFilesystemContentsAndQuota(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	var stores []*Store
	var filesystems []*memoryops.Filesystem
	for range 2 {
		files, err := memoryops.OpenFilesystem(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = files.Close() })
		stores = append(stores, newIntegrationStore(pool, WithMemoryFilesystem(files)))
		filesystems = append(filesystems, files)
	}
	admin := createSecretTestUser(t, ctx, stores[0], "Memory Files Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	resource, err := stores[0].Memories().Create(ctx, scope, "files", "", false)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, mustPublicID(t, publicid.KindOrganization, scope.OrgID),
		mustPublicID(t, publicid.KindProject, scope.ProjectID), resource.Name)
	if err := os.MkdirAll(filepath.Join(base, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte{0, 255, 1}
	if err := os.WriteFile(filepath.Join(base, "nested", ".existing.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	digest, body, err := stores[1].Memories().Read(ctx, scope, resource.ID, "nested/.existing.bin")
	if err != nil || !bytes.Equal(body, content) || digest != blobstore.ContentDigest(content) {
		t.Fatalf("filesystem content without file registration: %+v %q %v", digest, body, err)
	}
	for link, target := range map[string]string{"file-link": "nested/.existing.bin", "directory-link": "nested"} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"nested", "nested/.existing.bin/child", "file-link", "directory-link/new"} {
		if _, err := stores[1].Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: resource.ID, Path: name, Content: []byte("invalid"),
		}); !errors.Is(err, storeerr.ErrConflict) {
			t.Fatalf("file type validation for %s: %v", name, err)
		}
	}
	lock, err := filesystems[0].Lock(ctx, memoryFilesystemRef(t, scope, resource))
	if err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	_, _, readErr := stores[1].Memories().Read(readCtx, scope, resource.ID, "nested/.existing.bin")
	cancel()
	_ = lock.Close()
	if readErr != nil {
		t.Fatalf("read waited for writer lock: %v", readErr)
	}
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{"max_memories_per_store": 2})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range stores {
		wg.Go(func() {
			_, err := store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: fmt.Sprintf("new-%d.md", i), Content: []byte("new"),
			})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, storeerr.ErrConflict) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("shared filesystem quota: %d successful creations, want 1", succeeded)
	}
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{"max_memories_per_store": 0})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := dbsqlc.New(tx).LockMemoryStoreShared(ctx, dbsqlc.LockMemoryStoreSharedParams{
		ProjectID: scope.ProjectID, ID: resource.ID,
	}); err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := stores[1].Memories().Write(writeCtx, memorystore.WriteInput{
		Scope: scope, StoreID: resource.ID, Path: "nested/.existing.bin",
		Content: []byte("replacement"), ExpectedDigest: &digest,
	}); err != nil {
		t.Fatalf("replacement unnecessarily enforced creation quota: %v", err)
	}
}

func TestMemoryScopeDeletionRemovesDeletedStoreFiles(t *testing.T) {
	t.Parallel()
	for _, organization := range []bool{false, true} {
		t.Run(fmt.Sprintf("organization-%t", organization), func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			dir := t.TempDir()
			files, err := memoryops.OpenFilesystem(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = files.Close() }()
			store := newIntegrationStore(pool, WithMemoryFilesystem(files))
			admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
			scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
			resource, err := store.Memories().Create(ctx, scope, "old-store", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: "note.md", Content: []byte("retained content"),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := files.Stage(memoryFilesystemRef(t, scope, resource), []byte("unfinished upload")); err != nil {
				t.Fatal(err)
			}
			if err := dbsqlc.New(pool).DeleteMemoryStore(ctx, dbsqlc.DeleteMemoryStoreParams{
				ProjectID: scope.ProjectID, ID: resource.ID,
			}); err != nil {
				t.Fatal(err)
			}
			other, err := dbsqlc.New(pool).CreateProject(ctx, dbsqlc.CreateProjectParams{
				OrgID: scope.OrgID, Name: "Other project",
			})
			require.NoError(t, err)
			for _, projectID := range []uuid.UUID{scope.ProjectID, other.ID} {
				activeScope := scope
				activeScope.ProjectID = projectID
				_, err := store.Memories().Create(ctx, activeScope, "active-store", "", false)
				require.NoError(t, err)
			}
			if organization {
				_, err = store.Organizations().DeleteOrganization(ctx, scope.OrgID, scope.Principal)
			} else {
				_, err = store.Organizations().DeleteProject(ctx, scope.OrgID, scope.ProjectID, scope.Principal)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, projectID := range []uuid.UUID{scope.ProjectID, other.ID} {
				_, err := store.Memories().Resolve(ctx, projectID, "active-store")
				if organization || projectID == scope.ProjectID {
					require.ErrorIs(t, err, storeerr.ErrNotFound)
				} else {
					require.NoError(t, err)
				}
			}
			removed := mustPublicID(t, publicid.KindOrganization, scope.OrgID)
			if !organization {
				removed = filepath.Join(removed, mustPublicID(t, publicid.KindProject, scope.ProjectID))
			}
			for _, area := range []string{removed, filepath.Join(".staging", removed), filepath.Join(".locks", removed)} {
				if _, err := os.Stat(filepath.Join(dir, area)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted scope retained %s: %v", area, err)
				}
			}
		})
	}
}

func TestMemoryDeletionLogsCleanupFailure(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	for _, kind := range []string{"store", "project", "organization"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			ctx := log.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)))
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			dir := t.TempDir()
			files, err := memoryops.OpenFilesystem(dir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = files.Close() })
			store := newIntegrationStore(pool, WithMemoryFilesystem(files))
			admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
			scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
			resource, err := store.Memories().Create(ctx, scope, "notes", "", false)
			require.NoError(t, err)
			_, err = store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: "note.txt", Content: []byte("retained"),
			})
			require.NoError(t, err)
			root := filepath.Join(dir, mustPublicID(t, publicid.KindOrganization, scope.OrgID),
				mustPublicID(t, publicid.KindProject, scope.ProjectID), resource.Name)
			require.NoError(t, os.Chmod(root, 0500))
			t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			switch kind {
			case "store":
				err = store.Memories().Delete(ctx, scope, resource.ID)
			case "project":
				_, err = store.Organizations().DeleteProject(ctx, scope.OrgID, scope.ProjectID, scope.Principal)
			case "organization":
				_, err = store.Organizations().DeleteOrganization(ctx, scope.OrgID, scope.Principal)
			}
			require.NoError(t, err)
			_, err = store.Memories().Get(ctx, scope, resource.ID)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			require.Contains(t, logs.String(), `"level":"WARN"`)
			require.Contains(t, logs.String(), `"event.name":"memory.cleanup_failed"`)
			require.Contains(t, logs.String(), `"memory.cleanup.operation":"delete_`+kind+`"`)
			require.Contains(t, logs.String(), scope.OrgID.String())
			if kind != "organization" {
				require.Contains(t, logs.String(), scope.ProjectID.String())
			}
			if kind == "store" {
				require.Contains(t, logs.String(), resource.ID.String())
			}
		})
	}
}

func TestMemoryFileMutationsWaitForStoreDeletion(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store, files := newMemoryIntegrationStore(t, ctx, pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Lock Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	for _, operation := range []string{"write", "delete-file"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			resource, err := store.Memories().Create(ctx, scope, operation, "", false)
			require.NoError(t, err)
			input := memorystore.WriteInput{Scope: scope, StoreID: resource.ID, Path: "note.txt", Content: []byte("original")}
			result, err := store.Memories().Write(ctx, input)
			require.NoError(t, err)
			input.Content, input.ExpectedDigest = []byte("replacement"), &result.Digest
			tx := integrationdb.BeginTx(t, ctx, pool)
			q := dbsqlc.New(tx)
			_, err = q.LockMemoryStore(ctx, dbsqlc.LockMemoryStoreParams{ProjectID: scope.ProjectID, ID: resource.ID})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() {
				if operation == "delete-file" {
					done <- store.Memories().DeleteFile(ctx, scope, resource.ID, input.Path, result.Digest)
				} else {
					_, err := store.Memories().Write(ctx, input)
					done <- err
				}
			}()
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockMemoryStoreShared", 1)
			require.NoError(t, q.DeleteMemoryStore(ctx, dbsqlc.DeleteMemoryStoreParams{
				ProjectID: scope.ProjectID, ID: resource.ID,
			}))
			require.NoError(t, tx.Commit(ctx))
			require.ErrorIs(t, integrationdb.Await(t, done, "memory mutation after store deletion"), storeerr.ErrNotFound)
			root, err := files.OpenStore(memoryFilesystemRef(t, scope, resource))
			require.NoError(t, err)
			defer func() { _ = root.Close() }()
			content, err := memoryops.Read(root, input.Path)
			require.NoError(t, err)
			require.Equal(t, "original", string(content))
		})
	}
}
