//go:build integration && servicee2e && !windows

package e2e

import (
	"os"
	"os/exec"
	"testing"
)

func prepareServiceProcess(_ *testing.T, _ *exec.Cmd) {}

func signalServiceProcess(cmd *exec.Cmd, signal os.Signal) error {
	return cmd.Process.Signal(signal)
}
