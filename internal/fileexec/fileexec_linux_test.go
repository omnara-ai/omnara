package fileexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"golang.org/x/sys/unix"
)

func TestFileExecWithoutLandlock(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	runtime.LockOSThread()
	if err := seccomp.LoadFilter(seccomp.Filter{NoNewPrivs: true, Policy: seccomp.Policy{
		DefaultAction: seccomp.ActionAllow,
		Syscalls:      []seccomp.SyscallGroup{{Names: []string{"landlock_create_ruleset"}, Action: seccomp.ActionErrno}},
	}}); err != nil {
		t.Fatal(err)
	}
	args := os.Args[index+1:]
	if err := unix.Exec(args[0], args, os.Environ()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fileExecLauncher(t *testing.T) string {
	t.Helper()
	launcher := os.Getenv("OMNARA_TEST_FILE_EXEC")
	if launcher == "" {
		launcher = filepath.Join(t.TempDir(), "omnara-file-exec")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", launcher, "../../cmd/file-exec")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build file launcher: %s, %v", output, err)
		}
	}
	return launcher
}

func TestFileExecSeccomp(t *testing.T) {
	launcher := fileExecLauncher(t)
	probe := filepath.Join(t.TempDir(), "seccomp-probe")
	if compiled := os.Getenv("OMNARA_TEST_SECCOMP_PROBE"); compiled != "" {
		content, err := os.ReadFile(compiled)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(probe, content, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		build := exec.CommandContext(t.Context(), "cc", "-Wall", "-Wextra", "-Werror",
			"-o", probe, "testdata/seccomp_probe.c")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build seccomp probe: %s, %v", output, err)
		}
	}
	for _, mode := range []string{"unrestricted", "confined"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "allowed.txt")
			const content = "preserved"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			writable, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writable.Close() }()
			args := []string{mode, path, "5"}
			command := exec.CommandContext(t.Context(), probe, args...)
			if mode == "confined" {
				command = exec.CommandContext(t.Context(), launcher, append([]string{"1", probe}, args...)...)
			}
			command.ExtraFiles = []*os.File{root, root, writable}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("seccomp probe: %s, %v", output, err)
			}
			if mode == "confined" {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != content {
					t.Fatalf("file changed: %q, %v", got, err)
				}
			}
		})
	}
}

func TestFileExecConfinement(t *testing.T) {
	launcher := fileExecLauncher(t)
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	for _, name := range []string{"allowed/first.md", "allowed/nested/second.md", "second/other.md", "private/secret.md"} {
		path := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("TARGET "+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../private", filepath.Join(base, "allowed", "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(filepath.Join(base, "allowed"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	second, err := os.Open(filepath.Join(base, "second"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	viewPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(viewPath, "memory"), 0700); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"allowed", "second"} {
		if err := os.Symlink(fmt.Sprintf("/proc/self/fd/%d", i+4), filepath.Join(viewPath, "memory", name)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := os.Open(viewPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	for _, test := range []struct {
		name string
		args []string
		deny bool
	}{
		{"multiple stores", []string{"--json", "-e", "TARGET", "memory/allowed", "memory/second"}, false},
		{"parent traversal", []string{"--json", "-e", "TARGET", "memory/allowed/../private"}, true},
		{"absolute path", []string{"--json", "-e", "TARGET", filepath.Join(base, "private/secret.md")}, true},
		{"outward symlink", []string{"--json", "--follow", "-e", "TARGET", "memory/allowed/escape"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"2", rg,
				"--no-config", "--no-mmap", "--threads", "1", "--hidden", "--no-ignore"}
			command := exec.CommandContext(t.Context(), launcher, append(args, test.args...)...)
			command.ExtraFiles = []*os.File{view, root, second}
			command.Env = []string{"LANG=C.UTF-8"}
			output, err := command.CombinedOutput()
			if test.deny {
				if err == nil || !strings.Contains(string(output), "Permission denied") ||
					strings.Contains(string(output), "TARGET private") {
					t.Fatalf("access was not denied: %s, %v", output, err)
				}
			} else if err != nil ||
				strings.Count(string(output), `"type":"match"`) != 3 {
				t.Fatalf("whole-store search failed: %s, %v", output, err)
			}
		})
	}
	t.Run("pinned directory", func(t *testing.T) {
		if err := os.Rename(filepath.Join(base, "allowed"), filepath.Join(base, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(base, "allowed"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(base, "allowed", "secret.md"), []byte("TARGET REPLACEMENT\n"), 0600,
		); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), launcher, "2", rg,
			"--json", "--no-config", "--no-mmap", "--threads", "1", "-e", "TARGET", "memory/allowed", "memory/second")
		command.ExtraFiles = []*os.File{view, root, second}
		command.Env = []string{"LANG=C.UTF-8"}
		output, err := command.CombinedOutput()
		if err != nil || strings.Contains(string(output), "REPLACEMENT") ||
			strings.Count(string(output), `"type":"match"`) != 3 {
			t.Fatalf("searched replacement directory: %s, %v", output, err)
		}
	})
	t.Run("confinement required", func(t *testing.T) {
		command := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run=^TestFileExecWithoutLandlock$", "--", launcher, "1", rg, "-e", "TARGET", "memory/allowed")
		command.ExtraFiles = []*os.File{view, root}
		command.Env = []string{"LANG=C.UTF-8"}
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "missing kernel Landlock support") ||
			strings.Contains(string(output), "TARGET") {
			t.Fatalf("did not fail closed: %s, %v", output, err)
		}
	})
	t.Run("GC before exec", func(t *testing.T) {
		for i := range 20 {
			command := exec.CommandContext(t.Context(), launcher, "2", rg,
				"--json", "--no-config", "--no-mmap", "--threads", "1", "-e", "TARGET",
				"-e", strings.Repeat("x", 50*i+1), "--", "memory/allowed", "memory/second")
			command.ExtraFiles = []*os.File{view, root, second}
			command.Env = []string{"LANG=C.UTF-8", "GOGC=1"}
			output, err := command.CombinedOutput()
			if err != nil ||
				strings.Count(string(output), `"type":"match"`) != 3 {
				t.Fatalf("launch %d with GC pressure: %s, %v", i, output, err)
			}
		}
	})
}
