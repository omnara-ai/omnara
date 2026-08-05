package omnarad

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallLockSerializesAndReleases(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	lock, err := acquireInstallLock(context.Background(), home)
	if err != nil {
		t.Fatalf("acquire install lock: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, installLockFileName))
	if err != nil {
		t.Fatalf("stat install lock: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("install lock mode = %v, want private regular file", info.Mode())
	}
	if _, acquired, err := tryAcquireInstallLock(home); err != nil || acquired {
		t.Fatalf("contended try acquire = acquired %t error %v", acquired, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireInstallLock(ctx, home); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended install lock error = %v, want deadline exceeded", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release install lock: %v", err)
	}
	lock, err = acquireInstallLock(context.Background(), home)
	if err != nil {
		t.Fatalf("reacquire install lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release reacquired install lock: %v", err)
	}
}
