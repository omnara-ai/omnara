package integrationdb

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessIsRunning(t *testing.T) {
	if os.Getenv("OMNARA_TEST_PROCESS_LIFETIME_CHILD") == "1" {
		if _, err := os.Stdout.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if !processIsRunning(os.Getpid()) {
		t.Fatal("current process reported as stopped")
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProcessIsRunning$")
	command.Env = append(os.Environ(), "OMNARA_TEST_PROCESS_LIFETIME_CHILD=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	ready := make(chan error, 1)
	go func() { _, err := io.ReadFull(stdout, make([]byte, 1)); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start")
	}
	pid := command.Process.Pid
	if !processIsRunning(pid) {
		t.Fatal("live child reported as stopped")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if processIsRunning(pid) {
		t.Fatal("exited child reported as running")
	}
}
