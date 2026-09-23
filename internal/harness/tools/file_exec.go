package tools

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const fileExecStderrLimitBytes = 4 * 1024

func CheckFileToolSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := exec.LookPath("rg"); err != nil {
		return err
	}
	output, err := editFileText(ctx, []byte("é"), "s/./x/g")
	if err == nil && string(output) != "x" {
		return errors.New("script execution requires a working C.UTF-8 locale")
	}
	return err
}

type boundedStderrBuffer struct{ data []byte }

func (b *boundedStderrBuffer) Write(data []byte) (int, error) {
	n := len(data)
	b.data = append(b.data, data[:min(n, fileExecStderrLimitBytes-len(b.data))]...)
	return n, nil
}
