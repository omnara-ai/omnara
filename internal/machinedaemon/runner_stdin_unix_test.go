//go:build !windows

package machinedaemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/processcmd"
)

func TestPipeCommandWithoutStdinReadsEndOfInput(t *testing.T) {
	ctx := context.Background()
	bootstrap, _, _ := acceptedRunnerServerState(t)

	commandDir := t.TempDir()
	markerPath := filepath.Join(commandDir, "read-status")
	spec := processcmd.ForShell(`read line; printf '%s' "$?" > "$MARKER"`, "sh", "pipe")
	spec.Shell.NoStdin = true
	runner, err := prepareLocalRunner(ctx, bootstrap, ProcessAssignment{
		ID:      bootstrap.ProcessID,
		Process: Process{ExecutionSpec: spec, Cwd: commandDir},
		Env:     map[string]string{"MARKER": markerPath},
	})
	if err != nil {
		t.Fatalf("prepare command: %v", err)
	}
	t.Cleanup(func() { _ = runner.output.Close() })
	if _, _, err := startPreparedLocalRunner(runner); err != nil {
		t.Fatalf("start command: %v", err)
	}
	if runner.stdinOK {
		t.Fatal("a command without stdin accepts input")
	}

	// With an open stdin pipe, read would block until the test times out.
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := os.ReadFile(markerPath)
		if err == nil && len(status) > 0 {
			if strings.TrimSpace(string(status)) == "0" {
				t.Fatal("read got a line from a command without stdin")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read is still waiting for input")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := runner.WriteInput(ctx, "late\n"); err == nil {
		t.Fatal("wrote to a command without stdin")
	}
}
