//go:build integration

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestMemoryDirectoryAndDeleteConfinement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	dir := t.TempDir()
	files, err := memorystore.OpenFilesystem(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = files.Close() })
	store := newIntegrationStore(pool, WithMemoryFilesystem(files))
	admin := createSecretTestUser(t, ctx, store, "Memory Manager", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	memory, err := store.Memories().Create(ctx, scope, "notes", "", false)
	require.NoError(t, err)
	input := memorystore.WriteInput{Scope: scope, StoreID: memory.ID, Path: "nested/note.txt", Content: []byte("initial")}
	result, err := store.Memories().Write(ctx, input)
	require.NoError(t, err)
	viewer := createSecretTestUser(t, ctx, store, "Memory Viewer", authz.OrgRoleMember)
	_, err = store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: viewer.ID, Role: authz.ProjectRoleViewer,
	})
	require.NoError(t, err)
	viewerScope := scope
	viewerScope.Principal = userPrincipal(viewer.ID)
	_, err = store.Memories().ListDirectory(ctx, viewerScope, memory.ID, "", "", 50)
	require.NoError(t, err)
	_, _, err = store.Memories().Read(ctx, viewerScope, memory.ID, input.Path)
	require.NoError(t, err)
	denied := input
	denied.Scope = viewerScope
	_, err = store.Memories().Write(ctx, denied)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ErrorIs(t, store.Memories().DeleteFile(ctx, viewerScope, memory.ID, input.Path, result.Digest), storeerr.ErrNotFound)

	root := filepath.Join(dir, mustPublicID(t, publicid.KindOrganization, scope.OrgID),
		mustPublicID(t, publicid.KindProject, scope.ProjectID), memory.Name)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	page, err := store.Memories().ListDirectory(ctx, scope, memory.ID, "", "", 50)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "nested", page.Entries[0].Path)
	_, err = store.Memories().ListDirectory(ctx, scope, memory.ID, "escape", "", 50)
	require.Error(t, err)
	require.Error(t, store.Memories().DeleteFile(ctx, scope, memory.ID, "escape/secret", result.Digest))
	require.Error(t, store.Memories().DeleteFile(ctx, scope, memory.ID, "nested", result.Digest))
	content, err := os.ReadFile(filepath.Join(outside, "secret"))
	require.NoError(t, err)
	require.Equal(t, "secret", string(content))
	for _, name := range []string{"cleanup/deep/note.txt", "cleanup/keep.txt"} {
		_, err := store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: memory.ID, Path: name, Content: input.Content,
		})
		require.NoError(t, err)
	}
	require.NoError(t, store.Memories().DeleteFile(ctx, scope, memory.ID, "cleanup/deep/note.txt", result.Digest))
	_, err = os.Stat(filepath.Join(root, "cleanup", "deep"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, body, err := store.Memories().Read(ctx, scope, memory.ID, "cleanup/keep.txt")
	require.NoError(t, err)
	require.Equal(t, input.Content, body)
	require.NoError(t, store.Memories().DeleteFile(ctx, scope, memory.ID, "cleanup/keep.txt", result.Digest))
	_, err = os.Stat(filepath.Join(root, "cleanup"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, body, err = store.Memories().Read(ctx, scope, memory.ID, input.Path)
	require.NoError(t, err)
	require.Equal(t, input.Content, body)
	start, results := make(chan struct{}), make(chan error, 2)
	go func() {
		<-start
		results <- store.Memories().DeleteFile(ctx, scope, memory.ID, input.Path, result.Digest)
	}()
	go func() {
		<-start
		input.Content, input.ExpectedDigest = []byte("updated"), &result.Digest
		_, err := store.Memories().Write(ctx, input)
		results <- err
	}()
	close(start)
	first, second := <-results, <-results
	require.True(t, (first == nil) != (second == nil), "one mutation must succeed: %v, %v", first, second)
	require.True(t, errors.Is(first, storeerr.ErrConflict) || errors.Is(second, storeerr.ErrConflict))
}
