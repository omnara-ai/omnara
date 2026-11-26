package integrationlock

import (
	"os"
	"path/filepath"
	"testing"
)

func Acquire(t testing.TB) {
	t.Helper()

	path := filepath.Join(os.TempDir(), "omnara-integration-db.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open integration db lock: %v", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		t.Fatalf("acquire integration db lock: %v", err)
	}
	t.Cleanup(func() {
		_ = unlockFile(file)
		_ = file.Close()
	})
}
