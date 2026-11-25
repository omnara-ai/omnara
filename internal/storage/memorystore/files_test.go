package memorystore

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestFileReadError(t *testing.T) {
	for _, test := range []struct {
		name      string
		err, want error
	}{
		{"missing", fs.ErrNotExist, storeerr.ErrPathNotFound},
		{"parent is a file", fmt.Errorf("parent is a file: %w: %w", storeerr.ErrConflict, syscall.ENOTDIR),
			storeerr.ErrInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := fileReadError(test.err); !errors.Is(err, test.want) {
				t.Fatalf("fileReadError(%v) = %v, want %v", test.err, err, test.want)
			}
		})
	}
}
