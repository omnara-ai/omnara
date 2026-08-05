package integrationlock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func Acquire(t testing.TB) {
	t.Helper()

	path := filepath.Join(os.TempDir(), "omnara-integration-db.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open integration db lock: %v", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		t.Fatalf("acquire integration db lock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	})
}
