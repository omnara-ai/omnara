package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	sandboxStderrLimitBytes      = 4 * 1024
	sandboxSetupExitCode         = 125
	fileSearchUnavailableMessage = "file search is unavailable on this worker; " +
		"ask the operator to enable file-tool support"
	scriptEditUnavailableMessage = "scripted editing is unavailable on this worker; " +
		"use write_file with content and expected_digest to replace the file, or ask the operator to enable file-tool support"
)

func sandboxSetupFailed(err error, stderr []byte) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == sandboxSetupExitCode && len(stderr) > 0
}

func fileToolUnavailable(ctx context.Context, message string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	logent.WorkerFileToolsUnavailable(ctx, fmt.Errorf("%s: %w", message, cause))
	return storeerr.Tag(errFileToolUnavailable, errors.New(message))
}

func CheckFileToolSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "omnara-file-probe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("x"), 0600); err != nil {
		return err
	}
	root, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	source := searchSource{stores: []searchStore{{name: "probe", root: root}}}
	view, err := newSearchView(source.stores)
	if err != nil {
		return err
	}
	defer func() {
		_ = view.Close()
		_ = os.RemoveAll(view.Name())
	}()
	command, err := newSearchCommand(ctx,
		searchFilesRequest{Path: "/memory/probe/*.txt", Args: []string{"-e", "x"}}, source, view)
	if err != nil {
		return err
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("file search: %s (%w)", strings.TrimSpace(string(output)), err)
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
	b.data = append(b.data, data[:min(n, sandboxStderrLimitBytes-len(b.data))]...)
	return n, nil
}
