//go:build integration

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
			resource, err := memories.Create(
				ctx, scope, strings.ReplaceAll(principal.Type, "_", "-"), "", agentconfig.MemoryStoreAccessReadWrite,
			)
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
	original, err := store.Memories().Create(ctx, scope, "reuse", "", agentconfig.MemoryStoreAccessReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	oldInput := memorystore.WriteInput{
		Scope: scope, StoreID: original.ID, Path: "old.md", Content: []byte("old content"),
	}
	if _, err := store.Memories().Write(ctx, oldInput); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Memories().Create(
		ctx, scope, original.Name, "", agentconfig.MemoryStoreAccessReadWrite,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate creation: %v", err)
	}
	_, body, err := store.Memories().Read(ctx, scope, original.ID, oldInput.Path)
	if err != nil || !bytes.Equal(body, oldInput.Content) {
		t.Fatalf("duplicate creation damaged content: %q %v", body, err)
	}
	if err := store.Memories().Delete(ctx, scope, original.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.Memories().Create(
		ctx, scope, original.Name, "", agentconfig.MemoryStoreAccessReadWrite,
	)
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
		mustPublicID(t, publicid.KindMemoryStore, original.ID), oldInput.Path,
	)
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("deletion removed old files: %v", err)
	}
	if _, _, err := store.Memories().Read(ctx, scope, original.ID, oldInput.Path); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("deleted store stayed readable: %v", err)
	}
	_, _, readErr := store.Memories().Read(ctx, scope, replacement.ID, oldInput.Path)
	if !errors.Is(readErr, storeerr.ErrNotFound) {
		t.Fatalf("replacement inherited old file: %v", readErr)
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
	resource, err := stores[0].Memories().Create(ctx, scope, "files", "", agentconfig.MemoryStoreAccessReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, mustPublicID(t, publicid.KindOrganization, scope.OrgID),
		mustPublicID(t, publicid.KindProject, scope.ProjectID), mustPublicID(t, publicid.KindMemoryStore, resource.ID))
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
			resource, err := store.Memories().Create(
				ctx, scope, "old-store", "", agentconfig.MemoryStoreAccessReadWrite,
			)
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
				_, err := store.Memories().Create(
					ctx, activeScope, "active-store", "", agentconfig.MemoryStoreAccessReadWrite,
				)
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
			require.DirExists(t, filepath.Join(dir, removed))
			_, err = store.Memories().CleanupFiles(ctx)
			require.NoError(t, err)
			for _, area := range []string{removed, filepath.Join(".staging", removed), filepath.Join(".locks", removed)} {
				if _, err := os.Stat(filepath.Join(dir, area)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted scope retained %s: %v", area, err)
				}
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
			resource, err := store.Memories().Create(ctx, scope, operation, "", agentconfig.MemoryStoreAccessReadWrite)
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
			mutationErr := integrationdb.Await(t, done, "memory mutation after store deletion")
			require.ErrorIs(t, mutationErr, storeerr.ErrNotFound)
			require.EqualError(t, mutationErr, "memory store is unavailable: not found")
			root, err := files.OpenStore(memoryFilesystemRef(t, scope, resource))
			require.NoError(t, err)
			defer func() { _ = root.Close() }()
			content, err := memoryops.Read(root, input.Path)
			require.NoError(t, err)
			require.Equal(t, "original", string(content))
		})
	}
}

func TestMemoryPublishRechecksAgentAfterLockWait(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"archive", "detach"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store, _ := newMemoryIntegrationStore(t, ctx, pool)
			admin := createSecretTestUser(t, ctx, store, "Memory Agent Lock Admin", "admin")
			scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
			resource, err := store.Memories().Create(ctx, scope, "notes", "", agentconfig.MemoryStoreAccessReadWrite)
			require.NoError(t, err)
			input := memorystore.WriteInput{
				Scope: scope, StoreID: resource.ID, Path: "note.txt", Content: []byte("original"),
			}
			original, err := store.Memories().Write(ctx, input)
			require.NoError(t, err)
			source := testAgentConfigYAML() + "\nmemory_stores:\n  - name: notes\n    access: read_write\n"
			model := ensureTestConfiguredModelForSource(t, ctx, store, source)
			compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
				ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
					return resolvedTestModelSelection(model), nil
				},
				ResolveMemoryStoreName: func(string) (uuid.UUID, error) { return resource.ID, nil },
			})
			require.NoError(t, err)
			config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
				ProjectID: testProjectID, Source: source, SourceFormat: "yaml", ConfiguredModelID: model.ID,
				CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
				EffectiveDefinitionHash: compiled.Hash,
			})
			require.NoError(t, err)
			agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
				ProjectID: testProjectID, CurrentConfigID: config.ID,
			})
			require.NoError(t, err)
			detachedConfigID := mustCreateAgentConfig(t, ctx, store, testProjectID)
			tx := integrationdb.BeginTx(t, ctx, pool)
			_, err = dbsqlc.New(tx).LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
				ProjectID: testProjectID, ID: agent.ID,
			})
			require.NoError(t, err)
			input.Scope = memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, AgentID: agent.ID}
			input.Content, input.ExpectedDigest = []byte("replacement"), &original.Digest
			done := make(chan error, 1)
			go func() {
				_, err := store.Memories().Write(ctx, input)
				done <- err
			}()
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentForMemoryWrite", 1)
			if operation == "archive" {
				_, err = tx.Exec(ctx,
					`UPDATE agents SET state = 'archived', archived_at = statement_timestamp() WHERE id = $1`, agent.ID,
				)
			} else {
				_, err = tx.Exec(ctx, `UPDATE agents SET current_config_id = $1 WHERE id = $2`, detachedConfigID, agent.ID)
			}
			require.NoError(t, err)
			require.NoError(t, tx.Commit(ctx))
			require.ErrorIs(t, integrationdb.Await(t, done, "memory write after agent change"), storeerr.ErrNotFound)
			digest, content, err := store.Memories().Read(ctx, scope, resource.ID, input.Path)
			require.NoError(t, err)
			require.Equal(t, original.Digest, digest)
			require.Equal(t, "original", string(content))
		})
	}
}

func TestMemoryCleanupFiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	files, err := memoryops.OpenFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	create := func(name string) memorystore.Record {
		record, err := store.Memories().Create(ctx, scope, name, "", agentconfig.MemoryStoreAccessReadWrite)
		require.NoError(t, err)
		_, err = store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: record.ID, Path: "note.md", Content: []byte("content"),
		})
		require.NoError(t, err)
		return record
	}
	requireAreas := func(exists bool, record memorystore.Record) {
		path := filepath.Join(
			mustPublicID(t, publicid.KindOrganization, scope.OrgID),
			mustPublicID(t, publicid.KindProject, scope.ProjectID),
			mustPublicID(t, publicid.KindMemoryStore, record.ID),
		)
		for _, area := range []string{"", ".staging", ".locks"} {
			_, err := os.Stat(filepath.Join(dir, area, path))
			require.Equal(t, exists, err == nil, "%s %s: %v", area, record.Name, err)
		}
	}
	kept, deleted := create("kept"), create("deleted")
	require.NoError(t, store.Memories().Delete(ctx, scope, deleted.ID))
	stale, err := files.Stage(memoryFilesystemRef(t, scope, kept), []byte("abandoned"))
	require.NoError(t, err)
	fresh, err := files.Stage(memoryFilesystemRef(t, scope, kept), []byte("pending"))
	require.NoError(t, err)
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, stale), past, past))

	for _, want := range []memorystore.FileCleanupResult{{RemovedStores: 1, DiscardedStagedFiles: 1}, {}} {
		result, err := store.Memories().CleanupFiles(ctx)
		require.NoError(t, err)
		require.Equal(t, want, result)
	}
	requireAreas(false, deleted)
	requireAreas(true, kept)
	pending, err := dbsqlc.New(pool).ListMemoryStoresPendingFileRemoval(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoFileExists(t, filepath.Join(dir, stale))
	require.FileExists(t, filepath.Join(dir, fresh))
	_, body, err := store.Memories().Read(ctx, scope, kept.ID, "note.md")
	require.NoError(t, err)
	require.Equal(t, "content", string(body))
}

func TestMemoryCleanupFilesRetriesFailedRemoval(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	files, err := memoryops.OpenFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	folders := map[string]string{}
	for _, name := range []string{"blocked", "removable"} {
		record, err := store.Memories().Create(ctx, scope, name, "", agentconfig.MemoryStoreAccessReadWrite)
		require.NoError(t, err)
		_, err = store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: record.ID, Path: "note.md", Content: []byte("content"),
		})
		require.NoError(t, err)
		require.NoError(t, store.Memories().Delete(ctx, scope, record.ID))
		folders[name] = filepath.Join(
			dir,
			mustPublicID(t, publicid.KindOrganization, scope.OrgID),
			mustPublicID(t, publicid.KindProject, scope.ProjectID),
			mustPublicID(t, publicid.KindMemoryStore, record.ID),
		)
	}
	require.NoError(t, os.Chmod(folders["blocked"], 0500))
	t.Cleanup(func() { _ = os.Chmod(folders["blocked"], 0700) })

	result, err := store.Memories().CleanupFiles(ctx)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, 1, result.RemovedStores)
	require.NoDirExists(t, folders["removable"])
	require.DirExists(t, folders["blocked"])
	pending, err := dbsqlc.New(pool).ListMemoryStoresPendingFileRemoval(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	require.NoError(t, os.Chmod(folders["blocked"], 0700))
	result, err = store.Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.RemovedStores)
	require.NoDirExists(t, folders["blocked"])
}

func TestMemoryCleanupFilesRemovesLaterDeletedScope(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	files, err := memoryops.OpenFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	record, err := store.Memories().Create(ctx, scope, "notes", "", agentconfig.MemoryStoreAccessReadWrite)
	require.NoError(t, err)
	_, err = store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: record.ID, Path: "note.md", Content: []byte("content"),
	})
	require.NoError(t, err)
	require.NoError(t, store.Memories().Delete(ctx, scope, record.ID))
	_, err = store.Memories().CleanupFiles(ctx)
	require.NoError(t, err)

	org := mustPublicID(t, publicid.KindOrganization, scope.OrgID)
	project := filepath.Join(org, mustPublicID(t, publicid.KindProject, scope.ProjectID))
	folders := func(scope string) []string {
		return []string{
			filepath.Join(dir, scope),
			filepath.Join(dir, ".staging", scope),
			filepath.Join(dir, ".locks", scope),
		}
	}
	for _, folder := range folders(project) {
		require.DirExists(t, folder)
	}

	_, err = store.Organizations().DeleteProject(ctx, scope.OrgID, scope.ProjectID, scope.Principal)
	require.NoError(t, err)
	_, err = store.Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	for _, folder := range folders(project) {
		require.NoDirExists(t, folder)
	}
	for _, folder := range folders(org) {
		require.DirExists(t, folder)
	}

	_, err = store.Organizations().DeleteOrganization(ctx, scope.OrgID, scope.Principal)
	require.NoError(t, err)
	_, err = store.Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	for _, folder := range folders(org) {
		require.NoDirExists(t, folder)
	}
	result, err := store.Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	require.Equal(t, memorystore.FileCleanupResult{}, result)
}

func TestMemoryCleanupFilesRefusesUnpreparedDirectory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	unprepared := t.TempDir()
	unpreparedFiles, err := memoryops.OpenUnpreparedFilesystem(unprepared)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unpreparedFiles.Close() })
	maintenance := newIntegrationStore(pool, WithMemoryFilesystem(unpreparedFiles))

	result, err := maintenance.Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	require.Equal(t, memorystore.FileCleanupResult{}, result)

	dir := t.TempDir()
	files, err := memoryops.OpenFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Cleanup Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	record, err := store.Memories().Create(ctx, scope, "notes", "", agentconfig.MemoryStoreAccessReadWrite)
	require.NoError(t, err)
	_, err = store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: record.ID, Path: "note.md", Content: []byte("content"),
	})
	require.NoError(t, err)
	require.NoError(t, store.Memories().Delete(ctx, scope, record.ID))
	folder := filepath.Join(
		dir,
		mustPublicID(t, publicid.KindOrganization, scope.OrgID),
		mustPublicID(t, publicid.KindProject, scope.ProjectID),
		mustPublicID(t, publicid.KindMemoryStore, record.ID),
	)

	result, err = maintenance.Memories().CleanupFiles(ctx)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, memorystore.FileCleanupResult{}, result)
	pending, err := dbsqlc.New(pool).ListMemoryStoresPendingFileRemoval(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.DirExists(t, folder)
	entries, err := os.ReadDir(unprepared)
	require.NoError(t, err)
	require.Empty(t, entries)

	sharedFiles, err := memoryops.OpenUnpreparedFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sharedFiles.Close() })
	result, err = newIntegrationStore(pool, WithMemoryFilesystem(sharedFiles)).Memories().CleanupFiles(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.RemovedStores)
	require.NoDirExists(t, folder)
}
