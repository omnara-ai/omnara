package localstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestChildLockInheritanceHasNoOwnershipGap(t *testing.T) {
	const stageEnv = "OMNARA_CHILD_LOCK_TEST_STAGE"
	const pathEnv = "OMNARA_CHILD_LOCK_TEST_PATH"
	const readyEnv = "OMNARA_CHILD_LOCK_TEST_READY"
	const releaseEnv = "OMNARA_CHILD_LOCK_TEST_RELEASE"
	const fdEnv = "OMNARA_CHILD_LOCK_TEST_FD"
	if os.Getenv(stageEnv) == "child" {
		// Check inheritance before child code can adopt the descriptor.
		time.Sleep(250 * time.Millisecond)
		fd, err := strconv.Atoi(os.Getenv(fdEnv))
		if err != nil {
			t.Fatal(err)
		}
		lock, err := AdoptLock(os.Getenv(pathEnv), fd)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Release() }()
		if err := os.WriteFile(os.Getenv(readyEnv), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv(releaseEnv)); err == nil {
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatal("parent did not release child lock test")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "lifetime.lock")
	readyPath := filepath.Join(dir, "ready")
	releasePath := filepath.Join(dir, "release")
	lock, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("acquire parent lock: %v", err)
	}
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestChildLockInheritanceHasNoOwnershipGap$",
	)
	fd, restoreInheritance, err := lock.PrepareChildInheritance(command)
	if err != nil {
		_ = lock.Release()
		t.Fatalf("prepare child lock inheritance: %v", err)
	}
	command.Env = append(
		os.Environ(),
		stageEnv+"=child",
		pathEnv+"="+path,
		readyEnv+"="+readyPath,
		releaseEnv+"="+releasePath,
		fdEnv+"="+strconv.Itoa(fd),
	)
	startErr := func() error {
		defer restoreInheritance()
		return command.Start()
	}()
	if startErr != nil {
		_ = lock.Release()
		t.Fatalf("start child lock helper: %v", startErr)
	}
	childDone := make(chan struct{})
	var childErr error
	go func() {
		childErr = command.Wait()
		close(childDone)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(releasePath, nil, 0o600)
		_ = command.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(5 * time.Second):
		}
	})
	if err := lock.RelinquishInherited(); err != nil {
		t.Fatalf("relinquish parent lock copy: %v", err)
	}

	contender, err := TryAcquireLock(path)
	if contender != nil {
		_ = contender.Release()
	}
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf(
			"lock during pre-adoption child delay = %v, want ErrLockHeld",
			err,
		)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-childDone:
			t.Fatalf("child exited before adopting lock: %v", childErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not adopt inherited lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("release child lock helper: %v", err)
	}
	<-childDone
	if childErr != nil {
		t.Fatalf("child lock helper: %v", childErr)
	}
	reacquired, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("reacquire lock after child exit: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}
