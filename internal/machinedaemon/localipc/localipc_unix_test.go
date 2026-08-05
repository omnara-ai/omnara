//go:build !windows

package localipc

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSocketPathHashesBeforeDarwinLimit(t *testing.T) {
	endpoint := filepath.Join(
		t.TempDir(),
		"0123456789",
		"0123456789",
		"0123456789",
		"0123456789",
		"0123456789",
		"0123456789",
		"runner.sock",
	)
	if len(endpoint) <= maxDirectUnixSocketPath {
		t.Fatalf("test endpoint length = %d, want over threshold", len(endpoint))
	}
	got := socketPath(endpoint)
	if got == endpoint {
		t.Fatalf("socketPath did not hash endpoint length %d", len(endpoint))
	}
	if len(got) > maxUnixSocketPath {
		t.Fatalf("hashed socket path length = %d, want <= %d", len(got), maxUnixSocketPath)
	}
	if filepath.Dir(got) == os.TempDir() {
		t.Fatalf("hashed socket path parent = %q, must not use temp root directly", filepath.Dir(got))
	}
}

func TestLongSocketPathSecuresPerUserParent(t *testing.T) {
	// Use a short root so path hashing fits macOS's socket limit.
	root, err := os.MkdirTemp("/tmp", "oi-") //nolint:usetesting // see above
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	oldRoot := unixSocketRoot
	unixSocketRoot = root
	t.Cleanup(func() { unixSocketRoot = oldRoot })

	userRoot := filepath.Join(root, "omnara-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(userRoot, 0o777); err != nil {
		t.Fatalf("mkdir user root: %v", err)
	}
	endpoint := filepath.Join(
		root,
		"this",
		"is",
		"a",
		"very",
		"long",
		"endpoint",
		"path",
		"that",
		"exceeds",
		"the",
		"usual",
		"unix",
		"domain",
		"socket",
		"limit",
		"runner.sock",
	)
	listener, err := Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("listen long endpoint: %v", err)
	}
	_ = listener.Close()
	t.Cleanup(func() { _ = Cleanup(endpoint) })

	info, err := os.Stat(userRoot)
	if err != nil {
		t.Fatalf("stat user root: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("user root perms = %v, want 0700", info.Mode().Perm())
	}
}

func TestLocalIPCRejectsSymlinkParent(t *testing.T) {
	root := filepath.Join("/tmp", "omnara-ipc-symlink-"+time.Now().Format("150405000000000"))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	listener, err := Listen(context.Background(), filepath.Join(link, "runner.sock"))
	if err == nil {
		_ = listener.Close()
		t.Fatal("expected symlink parent to be rejected")
	}
}
