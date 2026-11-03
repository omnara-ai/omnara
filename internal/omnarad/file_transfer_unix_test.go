//go:build !windows

package omnarad

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestFileTransferUploadRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create fifo: %v", err)
	}
	err := runFileTransfer(context.Background(),
		processcmd.FileTransferUpload,
		fileTransferTestPublicID(t, publicid.KindProcess),
		path, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("fifo error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat fifo: %v", err)
	}
}
