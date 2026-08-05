//go:build !windows

package localstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestLockCanBeAdoptedAcrossExec(t *testing.T) {
	const stageEnv = "OMNARA_LOCK_EXEC_TEST_STAGE"
	const pathEnv = "OMNARA_LOCK_EXEC_TEST_PATH"
	const fdEnv = "OMNARA_LOCK_EXEC_TEST_FD"
	if stage := os.Getenv(stageEnv); stage != "" {
		path := os.Getenv(pathEnv)
		switch stage {
		case "prepare":
			lock, err := TryAcquireLock(path)
			if err != nil {
				t.Fatal(err)
			}
			fd, restore, err := lock.PrepareForExec()
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			t.Setenv(stageEnv, "adopt")
			t.Setenv(fdEnv, strconv.Itoa(fd))
			args := []string{os.Args[0], "-test.run=^TestLockCanBeAdoptedAcrossExec$"}
			if err := syscall.Exec(os.Args[0], args, os.Environ()); err != nil {
				t.Fatal(err)
			}
		case "adopt":
			fd, err := strconv.Atoi(os.Getenv(fdEnv))
			if err != nil {
				t.Fatal(err)
			}
			lock, err := AdoptLock(path, fd)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Release() }()
			contender, err := TryAcquireLock(path)
			if contender != nil {
				_ = contender.Release()
			}
			if !errors.Is(err, ErrLockHeld) {
				t.Fatalf("competing lock error = %v, want ErrLockHeld", err)
			}
		default:
			t.Fatalf("unknown exec test stage %q", stage)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "daemon.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockCanBeAdoptedAcrossExec$")
	cmd.Env = append(os.Environ(), stageEnv+"=prepare", pathEnv+"="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock exec helper: %v: %s", err, output)
	}
}
