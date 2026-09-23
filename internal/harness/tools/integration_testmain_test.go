//go:build integration

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}

func setupFileExec(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	dir := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build",
		"-o", filepath.Join(dir, "omnara-file-exec"), "../../../cmd/file-exec")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build file launcher: %s, %v", output, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
