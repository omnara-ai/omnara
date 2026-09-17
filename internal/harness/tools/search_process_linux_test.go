package tools

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
)

func TestMemorySearchProcess(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	if os.Getenv("OMNARA_TEST_BLOCK_LANDLOCK") == "1" {
		runtime.LockOSThread()
		if err := seccomp.LoadFilter(seccomp.Filter{NoNewPrivs: true, Policy: seccomp.Policy{
			DefaultAction: seccomp.ActionAllow,
			Syscalls:      []seccomp.SyscallGroup{{Names: []string{"landlock_create_ruleset"}, Action: seccomp.ActionErrno}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := RunSearchProcess(os.Args[index+1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func TestMemorySearchConfinement(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	for _, name := range []string{"allowed/first.md", "allowed/nested/second.md", "private/secret.md"} {
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
	for _, test := range []struct {
		name string
		args []string
		deny bool
	}{
		{"whole store", []string{"--json", "-e", "TARGET", "."}, false},
		{"parent traversal", []string{"--json", "-e", "TARGET", "../private"}, true},
		{"absolute path", []string{"--json", "-e", "TARGET", filepath.Join(base, "private/secret.md")}, true},
		{"outward symlink", []string{"--json", "--follow", "-e", "TARGET", "escape"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"-test.run=^TestMemorySearchProcess$", "--", rg,
				"--no-config", "--no-mmap", "--threads", "1", "--hidden", "--no-ignore"}
			command := exec.CommandContext(t.Context(), os.Args[0], append(args, test.args...)...)
			command.ExtraFiles = []*os.File{root}
			command.Env = []string{"LANG=C.UTF-8"}
			output, err := command.CombinedOutput()
			if test.deny {
				if err == nil || !strings.Contains(string(output), "Permission denied") || strings.Contains(string(output), "TARGET private") {
					t.Fatalf("access was not denied: %s, %v", output, err)
				}
			} else if err != nil || strings.Count(string(output), `"type":"match"`) != 2 {
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
		if err := os.WriteFile(filepath.Join(base, "allowed", "secret.md"), []byte("TARGET REPLACEMENT\n"), 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMemorySearchProcess$", "--", rg,
			"--json", "--no-config", "--no-mmap", "--threads", "1", "-e", "TARGET", ".")
		command.ExtraFiles = []*os.File{root}
		command.Env = []string{"LANG=C.UTF-8"}
		output, err := command.CombinedOutput()
		if err != nil || strings.Contains(string(output), "REPLACEMENT") || strings.Count(string(output), `"type":"match"`) != 2 {
			t.Fatalf("searched replacement directory: %s, %v", output, err)
		}
	})
	t.Run("confinement required", func(t *testing.T) {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMemorySearchProcess$", "--", rg, "-e", "TARGET", ".")
		command.ExtraFiles = []*os.File{root}
		command.Env = []string{"LANG=C.UTF-8", "OMNARA_TEST_BLOCK_LANDLOCK=1"}
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "missing kernel Landlock support") || strings.Contains(string(output), "TARGET") {
			t.Fatalf("did not fail closed: %s, %v", output, err)
		}
	})

}
