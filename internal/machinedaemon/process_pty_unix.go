//go:build !windows

package machinedaemon

import (
	"errors"
	"io"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

func startPTYProcessCommand(command *exec.Cmd, runner *localProcessRunner) error {
	ptyFile, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return err
	}
	runner.stdin = ptyFile
	runner.stdinOK = true
	runner.ptyMode = true
	runner.outputDone = make(chan struct{})
	go func() {
		defer close(runner.outputDone)
		_, copyErr := io.Copy(&runner.output, ptyFile)
		// PTY masters report EIO instead of EOF when the last slave closes.
		if errors.Is(copyErr, syscall.EIO) {
			copyErr = nil
		}
		runner.outputErr = errors.Join(copyErr, ptyFile.Close())
	}()
	return nil
}
