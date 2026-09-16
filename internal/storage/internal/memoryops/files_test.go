package memoryops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestFilePublication(t *testing.T) {
	files, err := OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	ref := testStoreRef(t, uuid.New(), "engineering")
	var root *os.Root
	write := func(name string, content []byte) error {
		t.Helper()
		staged, err := files.Stage(ref, content)
		if err != nil {
			return err
		}
		defer files.Discard(staged)
		lock, err := files.Lock(t.Context(), ref)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		return files.Publish(t.Context(), ref, root, name, staged)
	}
	content := []byte{0, 255, 1, 2}
	if err := write("nested/file.bin", content); err != nil {
		t.Fatal(err)
	}
	root, err = files.OpenStore(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	body, err := Read(root, "nested/file.bin")
	if err != nil || !bytes.Equal(body, content) {
		t.Fatalf("read: %v %v", body, err)
	}
	oldFile, err := root.Open("nested/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldFile.Close() }()
	for _, name := range []string{"nested", "nested/file.bin/child"} {
		if err := write(name, []byte("x")); err == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := write("nested/file.bin", nil); err != nil {
		t.Fatal(err)
	}
	old := make([]byte, len(content))
	if _, err := oldFile.Read(old); err != nil || !bytes.Equal(old, content) {
		t.Fatalf("replacement changed opened inode: %v %v", old, err)
	}
	if err := root.Symlink("nested", "link"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "link/file.bin"); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("symlink read: %v", err)
	}
	if err := write("link/file.bin", []byte("x")); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("symlink write: %v", err)
	}
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "file.bin")
	if err := os.WriteFile(outsidePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(outside, "escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "escape/file.bin"); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("external symlink read: %v", err)
	}
	if err := write("escape/file.bin", []byte("x")); err == nil {
		t.Fatalf("external symlink write: %v", err)
	}
	if body, err := os.ReadFile(outsidePath); err != nil || !bytes.Equal(body, content) {
		t.Fatalf("external file changed: %v %v", body, err)
	}
}

func TestSharedLock(t *testing.T) {
	ref := testStoreRef(t, uuid.New(), "shared")
	if dir := os.Getenv("OMNARA_TEST_MEMORY_LOCK_DIR"); dir != "" {
		files, err := OpenFilesystem(dir)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := files.Lock(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Close(); _ = files.Close() }()
		ready := os.NewFile(3, "ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := ready.Close(); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	dir := t.TempDir()
	files, err := OpenFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	readReady, writeReady, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readReady.Close(); _ = writeReady.Close() }()
	command := exec.Command(os.Args[0], "-test.run=^TestSharedLock$")
	command.ExtraFiles = []*os.File{writeReady}
	command.Env = append(os.Environ(), "OMNARA_TEST_MEMORY_LOCK_DIR="+dir)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if err := writeReady.Close(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() { _, err := io.ReadFull(readReady, make([]byte, 1)); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("child failed before acquiring lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not acquire lock")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	if _, err := files.Lock(ctx, ref); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process lock: %v", err)
	}
	inode, err := files.root.Stat(".locks/" + ref.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	lock, err := files.Lock(ctx, ref)
	if err != nil {
		t.Fatalf("lock after child termination: %v (%s)", err, output.String())
	}
	if err := files.RemoveStore(ref); err != nil {
		t.Fatal(err)
	}
	after, err := files.root.Stat(".locks/" + ref.path)
	if err != nil || !os.SameFile(inode, after) {
		t.Fatalf("purge changed lock inode: %v", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	replacement := testStoreRef(t, uuid.New(), "shared")
	if ref.staging == replacement.staging {
		t.Fatal("store incarnations share staging")
	}
	if _, err := files.Lock(ctx, replacement); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replacement bypassed existing lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedPublicationLeavesCurrentFile(t *testing.T) {
	files, err := OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	ref := testStoreRef(t, uuid.New(), "engineering")
	lock, err := files.Lock(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	content := []byte("original")
	staged, err := files.Stage(ref, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Publish(t.Context(), ref, nil, "file", staged); err != nil {
		t.Fatal(err)
	}
	staged, err = files.Stage(ref, []byte("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	files.Discard(staged)
	err = files.Publish(t.Context(), ref, nil, "file", staged)
	if err == nil {
		t.Fatalf("expected rename failure: %v", err)
	}
	root, err := files.OpenStore(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	body, err := Read(root, "file")
	if err != nil || !bytes.Equal(body, content) {
		t.Fatalf("failed publication changed current file: %q %v", body, err)
	}
}

func testStoreRef(t *testing.T, id uuid.UUID, name string) StoreRef {
	t.Helper()
	ref, err := NewStoreRef(
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		id,
		name,
	)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestStoreLayoutAndIsolation(t *testing.T) {
	dir := t.TempDir()
	files, err := OpenFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	orgID, projectID := uuid.New(), uuid.New()
	var first StoreRef
	paths := make(map[string][]byte)
	for _, scope := range []struct {
		org, project uuid.UUID
		name         string
	}{
		{orgID, projectID, "engineering"},
		{orgID, projectID, "operations"},
		{orgID, uuid.New(), "engineering"},
		{uuid.New(), projectID, "engineering"},
	} {
		ref, err := NewStoreRef(scope.org, scope.project, uuid.New(), scope.name)
		if err != nil {
			t.Fatal(err)
		}
		if first.path == "" {
			first = ref
		}
		body := []byte(ref.path)
		upload, err := files.Stage(ref, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := files.Publish(t.Context(), ref, nil, "nested/note.md", upload); err != nil {
			t.Fatal(err)
		}
		org, err := publicid.Encode(publicid.KindOrganization, scope.org)
		if err != nil {
			t.Fatal(err)
		}
		project, err := publicid.Encode(publicid.KindProject, scope.project)
		if err != nil {
			t.Fatal(err)
		}
		physical := filepath.Join(dir, org, project, scope.name, "nested", "note.md")
		paths[physical] = body
		entries, err := os.ReadDir(filepath.Join(dir, ref.staging))
		if err != nil || len(entries) != 0 {
			t.Fatalf("published upload still staged: %v %v", entries, err)
		}
	}
	if err := files.RemoveStore(first); err != nil {
		t.Fatal(err)
	}
	for physical, want := range paths {
		got, err := os.ReadFile(physical)
		if string(want) == first.path {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted store remains: %v", err)
			}
		} else if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("physical layout or isolation: %q %v", got, err)
		}
	}

	for _, name := range []string{"../other", "a/b", ".invalid", ""} {
		if _, err := NewStoreRef(orgID, projectID, uuid.New(), name); err == nil {
			t.Fatalf("accepted store name %q", name)
		}
	}
}

func TestRemoveScopeIsolation(t *testing.T) {
	files, err := OpenFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	org, otherOrg, project, otherProject := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var refs []StoreRef
	for _, scope := range []struct{ org, project uuid.UUID }{{org, project}, {org, otherProject}, {otherOrg, project}} {
		ref, err := NewStoreRef(scope.org, scope.project, uuid.New(), "notes")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
		upload, err := files.Stage(ref, []byte("content"))
		if err != nil {
			t.Fatal(err)
		}
		if err := files.Publish(t.Context(), ref, nil, "note.md", upload); err != nil {
			t.Fatal(err)
		}
		if _, err := files.Stage(ref, []byte("staged")); err != nil {
			t.Fatal(err)
		}
	}
	for step, projectID := range []*uuid.UUID{&project, nil} {
		if err := files.RemoveScope(org, projectID); err != nil {
			t.Fatal(err)
		}
		if err := files.RemoveScope(org, projectID); err != nil {
			t.Fatalf("cleanup retry: %v", err)
		}
		for i, ref := range refs {
			for _, name := range []string{ref.path, ref.staging} {
				_, err := files.root.Stat(name)
				if i <= step {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("scope survived cleanup: %s %v", name, err)
					}
				} else if err != nil {
					t.Fatalf("cleanup affected another scope: %s %v", name, err)
				}
			}
		}
	}
}

func TestNFSMissingPaths(t *testing.T) {
	clientA := os.Getenv("OMNARA_TEST_MEMORY_NFS_A")
	clientB := os.Getenv("OMNARA_TEST_MEMORY_NFS_B")
	server := os.Getenv("OMNARA_TEST_MEMORY_NFS_SERVER")
	if clientA == "" && clientB == "" && server == "" {
		t.Skip("requires two independently cached NFS mounts and their server backing directory")
	}
	if clientA == "" || clientB == "" || server == "" {
		t.Fatal("set OMNARA_TEST_MEMORY_NFS_A, OMNARA_TEST_MEMORY_NFS_B, and OMNARA_TEST_MEMORY_NFS_SERVER")
	}
	for _, operation := range []string{"open", "replacement-cleanup", "project-cleanup", "org-cleanup"} {
		t.Run(operation, func(t *testing.T) {
			a, err := OpenFilesystem(clientA)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			b, err := OpenFilesystem(clientB)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = b.Close() }()
			orgID, projectID := uuid.New(), uuid.New()
			ref, err := NewStoreRef(orgID, projectID, uuid.New(), "notes")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.OpenStore(ref); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("prime missing: %v", err)
			}
			staged, err := a.Stage(ref, []byte("first writer"))
			if err != nil {
				t.Fatal(err)
			}
			lock, err := a.Lock(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			if err = a.Publish(t.Context(), ref, nil, "note.md", staged); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "open":
				stagedB, stageErr := b.Stage(ref, []byte("second writer"))
				if stageErr != nil {
					t.Fatal(stageErr)
				}
				defer b.Discard(stagedB)
				lockB, lockErr := b.Lock(t.Context(), ref)
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer func() { _ = lockB.Close() }()
				root, openErr := b.OpenStore(ref)
				if openErr != nil {
					t.Fatalf("missed published file while holding store lock: %v", openErr)
				}
				defer func() { _ = root.Close() }()
				body, readErr := Read(root, "note.md")
				if readErr != nil || string(body) != "first writer" {
					t.Fatalf("read %q: %v", body, readErr)
				}
				return
			case "replacement-cleanup":
				replacement, refErr := NewStoreRef(orgID, projectID, uuid.New(), "notes")
				if refErr != nil {
					t.Fatal(refErr)
				}
				lockB, lockErr := b.Lock(t.Context(), replacement)
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer func() { _ = lockB.Close() }()
				err = b.RemoveStore(replacement)
			case "project-cleanup":
				err = b.RemoveScope(orgID, &projectID)
			case "org-cleanup":
				err = b.RemoveScope(orgID, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(server, ref.path, "note.md"))
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("cleanup left old contents on server: %v", err)
			}
		})
	}
}
