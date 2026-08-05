//go:build darwin || linux

package omnarad

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cancelWriter struct {
	strings.Builder
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(body []byte) (int, error) {
	w.cancel()
	return w.Builder.Write(body)
}

func TestEnsureServiceLogRejectsSymlink(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	logDir := filepath.Join(home, "logs", "daemon")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("create target log: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(logDir, "daemon.log")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ensureServiceLog(home); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("ensure service log error = %v", err)
	}
}

func TestCreateOwnedDirectoryAllPreservesExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create service directory: %v", err)
	}
	if err := createOwnedDirectoryAll(path, 0o700); err != nil {
		t.Fatalf("validate service directory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat service directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("service directory mode = %o, want 755", got)
	}
}

func writeTestExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod executable %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func shellTestQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
