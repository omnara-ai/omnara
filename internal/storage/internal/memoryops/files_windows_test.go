package memoryops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/testfs"
	"golang.org/x/sys/windows"
)

func TestWindowsMemoryRequiresCaseSensitivity(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = checkCaseSensitive(file)
	_ = file.Close()
	if err == nil {
		t.Skip("the host's temporary directory already inherits case sensitivity")
	}
	if files, err := OpenFilesystem(dir); err == nil {
		_ = files.Close()
		t.Fatal("accepted a case-insensitive memory directory")
	} else if !strings.Contains(err.Error(), "case-sensitive") {
		t.Fatal(err)
	}
	testfs.Prepare(t, dir)
	files, err := OpenFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = files.Close()
}

func TestWindowsMemoryNamesAndAliases(t *testing.T) {
	dir := testfs.TempDir(t)
	files, err := OpenFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	ref := testStoreRef(t, "notes")
	write := func(name, content string) error {
		upload, err := files.Stage(ref, []byte(content))
		if err != nil {
			return err
		}
		defer func() { _ = files.Discard(upload) }()
		return files.Publish(t.Context(), ref, nil, name, upload)
	}
	for _, name := range []string{
		"nested/note.txt", "nested/NOTE.txt", "nested/literal~1.txt", "long-directory-name/long-memory-file-name.txt",
	} {
		if err := write(name, name); err != nil {
			t.Fatal(err)
		}
	}
	root, err := files.OpenStore(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"nested/note.txt", "nested/NOTE.txt", "nested/literal~1.txt"} {
		if content, err := Read(root, name); err != nil || string(content) != name {
			t.Fatalf("distinct filename %s: %q, %v", name, content, err)
		}
	}
	const original = "long-directory-name/long-memory-file-name.txt"
	shortName := func(relative string) string {
		t.Helper()
		full, err := windows.UTF16PtrFromString(filepath.Join(dir, ref.path, relative))
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]uint16, 32768)
		n, err := windows.GetShortPathName(full, &buffer[0], uint32(len(buffer)))
		if err != nil || n >= uint32(len(buffer)) {
			t.Fatalf("short path: %d, %v", n, err)
		}
		return filepath.Base(windows.UTF16ToString(buffer[:n]))
	}
	directoryAlias := shortName("long-directory-name")
	fileAlias := shortName(original)
	if directoryAlias == "long-directory-name" || fileAlias == "long-memory-file-name.txt" {
		t.Skip("8.3 alias generation is disabled on the test volume")
	}
	for _, name := range []string{
		"long-directory-name/" + fileAlias,
		directoryAlias + "/long-memory-file-name.txt",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(root, name); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("alias read: %v", err)
			}
			if err := write(name, "replacement"); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("alias replacement: %v", err)
			}
			if err := files.RemoveFile(ref, root, name); !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("alias deletion: %v", err)
			}
			if content, err := Read(root, original); err != nil || string(content) != original {
				t.Fatalf("alias modified original: %q, %v", content, err)
			}
		})
	}
}

func TestWindowsReplacedFileIsNotAnAlias(t *testing.T) {
	files, err := OpenFilesystem(testfs.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	ref := testStoreRef(t, "concurrent")
	staged, err := files.Stage(ref, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Publish(t.Context(), ref, nil, "file.txt", staged); err != nil {
		t.Fatal(err)
	}
	root, err := files.OpenStore(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	old, err := root.Open("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	staged, err = files.Stage(ref, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Publish(t.Context(), ref, root, "file.txt", staged); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeFilename(root, "file.txt", old); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("replaced file should trigger a fresh filename check: %v", err)
	}
	if content, err := Read(root, "file.txt"); err != nil || string(content) != "new" {
		t.Fatalf("read replacement: %q, %v", content, err)
	}
}
