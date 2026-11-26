package memorystore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/testfs"
)

func TestFileReadError(t *testing.T) {
	dir := testfs.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, test := range []struct {
		path          string
		want, notWant error
	}{
		{"missing", storeerr.ErrPathNotFound, storeerr.ErrInvalidRequest},
		{"file/child", storeerr.ErrInvalidRequest, storeerr.ErrNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			_, readErr := memoryops.Read(root, test.path)
			if err := fileReadError(readErr); !errors.Is(err, test.want) || errors.Is(err, test.notWant) {
				t.Fatalf("fileReadError(%v) = %v, want %v without %v", readErr, err, test.want, test.notWant)
			}
		})
	}
}
