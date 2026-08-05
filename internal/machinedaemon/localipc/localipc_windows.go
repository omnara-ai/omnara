//go:build windows

package localipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

func listen(_ context.Context, endpoint string) (Listener, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("local ipc endpoint is required")
	}
	return winio.ListenPipe(
		pipeName(endpoint),
		&winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;OW)"},
	)
}

func dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipeName(endpoint))
}

func cleanup(endpoint string) error {
	return nil
}

func pipeName(endpoint string) string {
	if strings.HasPrefix(endpoint, `\\.\pipe\`) {
		return endpoint
	}
	sum := sha256.Sum256([]byte(endpoint))
	return `\\.\pipe\omnara-` + hex.EncodeToString(sum[:16])
}
