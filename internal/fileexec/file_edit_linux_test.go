package fileexec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFileEditWithoutPrivileges(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	args := os.Args[index+1:]
	if err := unix.Exec(args[0], args, os.Environ()); err != nil {
		t.Fatal(err)
	}
}

func TestFileEditConfinement(t *testing.T) {
	launcher := os.Getenv("OMNARA_TEST_FILE_EDIT")
	if launcher == "" {
		t.Skip("run with the installed editor in test-worker-image")
	}
	if os.Getuid() == 0 {
		t.Fatal("confinement must be tested from a non-root parent")
	}
	for _, test := range []struct {
		name, script, input, want, diagnostic string
	}{
		{name: "edit", script: "s/foo/bar/", input: "foo\n", want: "bar\n"},
		{name: "unicode", script: "s/./x/g", input: "é", want: "x"},
		{
			name: "unicode replacements", script: `s/[éè]/e/g; s/[“”]/"/g; s/[—–]/-/g`,
			input: "café “hi” — ok", want: `cafe "hi" - ok`,
		},
		{name: "empty", script: "s/foo/bar/"},
		{name: "execution", script: "e id", input: "x", diagnostic: "disabled in sandbox mode"},
		{name: "read", script: "r /etc/passwd", input: "x", diagnostic: "disabled in sandbox mode"},
		{name: "write", script: "w /tmp/out", input: "x", diagnostic: "disabled in sandbox mode"},
		{name: "argument is script", script: "--help", diagnostic: "unknown command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), launcher, test.script)
			command.Stdin = strings.NewReader(test.input)
			output, err := command.CombinedOutput()
			if test.diagnostic != "" {
				if err == nil || !strings.Contains(string(output), test.diagnostic) {
					t.Fatalf("expected rejection: %q, %v", output, err)
				}
			} else if err != nil || string(output) != test.want {
				t.Fatalf("output = %q, error = %v; want %q", output, err, test.want)
			}
		})
	}
	t.Run("long scripts", func(t *testing.T) {
		for i := range 20 {
			command := exec.CommandContext(t.Context(), launcher, "s/foo/bar/;#"+strings.Repeat("x", 3000*i))
			command.Stdin = strings.NewReader("foo\n")
			output, err := command.CombinedOutput()
			if err != nil || string(output) != "bar\n" {
				t.Fatalf("script launch %d: %s, %v", i, output, err)
			}
		}
	})
	t.Run("extra argument", func(t *testing.T) {
		command := exec.CommandContext(t.Context(), launcher, "", "/etc/passwd")
		if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "one script argument") {
			t.Fatalf("accepted extra argument: %q, %v", output, err)
		}
	})
	t.Run("privilege required", func(t *testing.T) {
		command := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run=^TestFileEditWithoutPrivileges$", "--", launcher, "s/foo/bar/")
		command.Stdin = strings.NewReader("foo")
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "chroot") || strings.Contains(string(output), "bar") {
			t.Fatalf("did not fail closed: %q, %v", output, err)
		}
	})
	t.Run("process boundary and cancellation", func(t *testing.T) {
		outside, err := os.CreateTemp(t.TempDir(), "outside")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = outside.Close() }()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, launcher, ":a;ba")
		command.Stdin = strings.NewReader("x\n")
		command.ExtraFiles = []*os.File{outside}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				cancel()
				_ = command.Wait()
			}
		})
		proc := filepath.Join("/proc", strconv.Itoa(command.Process.Pid))
		var status []byte
		for {
			status, err = os.ReadFile(filepath.Join(proc, "status"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(string(status), "Name:\tsed\n") {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("sed did not start: %s", status)
			case <-time.After(10 * time.Millisecond):
			}
		}
		for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapAmb"} {
			if !strings.Contains(string(status), field+":\t0000000000000000\n") {
				t.Fatalf("retained capabilities: %s", status)
			}
		}
		if !strings.Contains(string(status), "NoNewPrivs:\t1\n") {
			t.Fatalf("no_new_privs missing: %s", status)
		}
		root, err := os.Readlink(filepath.Join(proc, "root"))
		if err != nil || root != editRoot {
			t.Fatalf("root = %q, error = %v", root, err)
		}
		cwd, err := os.Readlink(filepath.Join(proc, "cwd"))
		if err != nil || cwd != root {
			t.Fatalf("working directory = %q, error = %v", cwd, err)
		}
		for _, path := range []string{outside.Name(), "/proc"} {
			if _, err := os.Stat(filepath.Join(proc, "root") + path); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unexpected path in jail %q: %v", path, err)
			}
		}
		fds, err := os.ReadDir(filepath.Join(proc, "fd"))
		if err != nil {
			t.Fatal(err)
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(proc, "fd", fd.Name()))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if target == outside.Name() {
				t.Fatalf("inherited descriptor %s retained access to %s", fd.Name(), target)
			}
		}
		cancel()
		if err := command.Wait(); err == nil || command.ProcessState == nil || command.ProcessState.Success() {
			t.Fatalf("cancellation failed: %v", err)
		}
	})
}
