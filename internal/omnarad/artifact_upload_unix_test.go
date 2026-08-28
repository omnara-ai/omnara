//go:build !windows

package omnarad

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestRunUploadArtifactCommandRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create fifo: %v", err)
	}
	err := runUploadArtifactCommand(
		context.Background(),
		artifactUploadTestPublicID(t, publicid.KindToolCall),
		base64.RawURLEncoding.EncodeToString([]byte(path)),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("fifo error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat fifo: %v", err)
	}
}
