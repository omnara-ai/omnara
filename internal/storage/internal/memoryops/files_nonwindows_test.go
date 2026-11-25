//go:build !windows

package memoryops

import (
	"io/fs"
	"os"
	"sync"
	"testing"
)

func blockStoreRemoval(t *testing.T, files *Filesystem, ref StoreRef) (func(), error) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	if err := files.root.Chmod(ref.path, 0500); err != nil {
		t.Fatal(err)
	}
	return sync.OnceFunc(func() {
		if err := files.root.Chmod(ref.path, 0700); err != nil {
			t.Error(err)
		}
	}), fs.ErrPermission
}
