//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

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
	ref, err := memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, record.ID, record.Name)
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
	for range 2 {
		files, err := memoryops.OpenFilesystem(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = files.Close() })
		stores = append(stores, newIntegrationStore(pool, WithMemoryFilesystem(files)))
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
	lock, err := stores[0].memoryFS.Lock(ctx, memoryFilesystemRef(t, scope, resource))
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
	if _, err := dbsqlc.New(tx).LockMemoryStoreForConfig(ctx, dbsqlc.LockMemoryStoreForConfigParams{
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
			if organization {
				_, err = store.Organizations().DeleteOrganization(ctx, scope.OrgID, scope.Principal)
			} else {
				_, err = store.Organizations().DeleteProject(ctx, scope.OrgID, scope.ProjectID, scope.Principal)
			}
			if err != nil {
				t.Fatal(err)
			}
			removed := mustPublicID(t, publicid.KindOrganization, scope.OrgID)
			if !organization {
				removed = filepath.Join(removed, mustPublicID(t, publicid.KindProject, scope.ProjectID))
			}
			for _, area := range []string{removed, filepath.Join(".staging", removed)} {
				if _, err := os.Stat(filepath.Join(dir, area)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted scope retained %s: %v", area, err)
				}
			}
		})
	}
}
