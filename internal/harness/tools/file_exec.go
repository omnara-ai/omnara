package tools

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func CheckFileToolSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := exec.LookPath("rg"); err != nil {
		return err
	}
	_, err := editFileText(ctx, nil, "")
	return err
}

func newFileExecCommand(ctx context.Context, name string, roots []*os.File, args ...string) (*exec.Cmd, error) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "omnara-file-exec",
		append([]string{strconv.Itoa(len(roots)), binary}, args...)...)
	command.ExtraFiles = roots
	return command, nil
}

type boundedStderrBuffer struct{ data []byte }

func (b *boundedStderrBuffer) Write(data []byte) (int, error) {
	n := len(data)
	b.data = append(b.data, data[:min(n, 4096-len(b.data))]...)
	return n, nil
}
