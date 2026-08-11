package localstore

import (
	"errors"
	"io"
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
	const fdEnv = "OMNARA_CHILD_LOCK_TEST_FD"
	if os.Getenv(stageEnv) == "child" {
		fd, err := strconv.Atoi(os.Getenv(fdEnv))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stdout.Write([]byte("R")); err != nil {
			t.Fatal(err)
		}
		proceed := make([]byte, 1)
		if _, err := io.ReadFull(os.Stdin, proceed); err != nil {
			t.Fatal(err)
		}
		lock, err := AdoptLock(os.Getenv(pathEnv), fd)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Release() }()
		if _, err := os.Stdout.Write([]byte("A")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(os.Stdin, proceed); !errors.Is(err, io.EOF) {
			t.Fatalf("parent did not release child lock test: %v", err)
		}
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "lifetime.lock")
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
		fdEnv+"="+strconv.Itoa(fd),
	)
	childStdin, err := command.StdinPipe()
	if err != nil {
		_ = lock.Release()
		t.Fatalf("open child stdin: %v", err)
	}
	childStdout, err := command.StdoutPipe()
	if err != nil {
		_ = lock.Release()
		t.Fatalf("open child stdout: %v", err)
	}
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
		_ = childStdin.Close()
		_ = command.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(5 * time.Second):
		}
	})
	childSignal := func(what string) byte {
		signal := make([]byte, 1)
		if _, err := io.ReadFull(childStdout, signal); err != nil {
			select {
			case <-childDone:
				t.Fatalf(
					"child exited before %s: read=%v wait=%v",
					what,
					err,
					childErr,
				)
			default:
			}
			t.Fatalf("read child %s signal: %v", what, err)
		}
		return signal[0]
	}
	if err := lock.RelinquishInherited(); err != nil {
		t.Fatalf("relinquish parent lock copy: %v", err)
	}

	if signal := childSignal("pre-adoption readiness"); signal != 'R' {
		t.Fatalf("pre-adoption readiness signal = %q", signal)
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

	if _, err := childStdin.Write([]byte("G")); err != nil {
		t.Fatalf("release child adoption: %v", err)
	}
	if signal := childSignal("lock adoption"); signal != 'A' {
		t.Fatalf("lock adoption signal = %q", signal)
	}
	if err := childStdin.Close(); err != nil {
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
