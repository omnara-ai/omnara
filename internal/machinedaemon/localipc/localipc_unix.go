//go:build !windows

package localipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func listen(ctx context.Context, endpoint string) (Listener, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("local ipc endpoint is required")
	}
	endpoint = socketPath(endpoint)
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return nil, err
	}
	if err := ensurePrivatePath(filepath.Dir(endpoint)); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", endpoint)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath(endpoint))
}

func cleanup(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if err := os.Remove(socketPath(endpoint)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

const (
	maxDirectUnixSocketPath = 90
	maxUnixSocketPath       = 103
)

var unixSocketRoot = "/tmp"

func socketPath(endpoint string) string {
	if len(endpoint) <= maxDirectUnixSocketPath {
		return endpoint
	}
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(
		unixSocketRoot,
		"omnara-"+strconv.Itoa(os.Getuid()),
		"ipc",
		hex.EncodeToString(sum[:16])+".sock",
	)
}

func ensurePrivateDir(dir string) error {
	info, err := os.Lstat(filepath.Clean(dir))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local ipc parent must not be a symlink: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("local ipc parent is not a directory: %s", dir)
	}
	return os.Chmod(filepath.Clean(dir), 0o700)
}

func ensurePrivatePath(dir string) error {
	dir = filepath.Clean(dir)
	root := filepath.Clean(unixSocketRoot)
	if rel, err := filepath.Rel(
		root,
		dir,
	); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) &&
		rel != ".." {
		first, _, _ := strings.Cut(rel, string(os.PathSeparator))
		if first == "omnara-"+strconv.Itoa(os.Getuid()) {
			if err := ensurePrivateDir(filepath.Join(root, first)); err != nil {
				return err
			}
		}
	}
	return ensurePrivateDir(dir)
}
