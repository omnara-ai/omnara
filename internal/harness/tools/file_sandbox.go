package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		"use storage_write with content to replace the file, or ask the operator to enable file-tool support"
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

func CheckFileSearchSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		return checkNativeFileSearchSupport(ctx)
	}
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
		storageSearchRequest{Path: "/memory/probe/*.txt", Args: []string{"-e", "x"}}, source, view)
	if err != nil {
		return err
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("file search: %s (%w)", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func checkNativeFileSearchSupport(ctx context.Context) error {
	line := 1
	output := searchOutput{input: storageSearchRequest{Args: []string{"-e", "x"}, Limit: 1, OffsetLine: &line}}
	if err := output.search(ctx, searchSource{path: "/artifacts/probe", content: []byte("x")}); err != nil {
		return err
	}
	if output.result.MatchCount != 1 {
		return errors.New("native file search did not return the probe match")
	}
	return nil
}

func CheckScriptEditSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	text, err := editFileText(ctx, []byte("é😀\na\r\n"), "1s/./x/g;2s/a/b/")
	if err != nil {
		return err
	}
	if string(text) != "xx\nb\r\n" {
		return errors.New("scripted editing requires a working C.UTF-8 locale and binary I/O")
	}
	return nil
}

type boundedStderrBuffer struct{ data []byte }

func (b *boundedStderrBuffer) Write(data []byte) (int, error) {
	n := len(data)
	b.data = append(b.data, data[:min(n, sandboxStderrLimitBytes-len(b.data))]...)
	return n, nil
}
