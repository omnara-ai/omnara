package machinedaemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestReadDaemonHTTPResponseEnforcesProtocolLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat("x", daemonprotocol.MaxMessageBytes+1))
	data, err := readDaemonHTTPResponse(body)
	if !errors.Is(err, errDaemonHTTPResponseTooLarge) {
		t.Fatalf("read error = %v, want response limit error", err)
	}
	if len(data) != daemonprotocol.MaxMessageBytes {
		t.Fatalf("retained bytes = %d, want %d", len(data), daemonprotocol.MaxMessageBytes)
	}
}
