package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const fileExecStderrLimitBytes = 4 * 1024

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
	b.data = append(b.data, data[:min(n, fileExecStderrLimitBytes-len(b.data))]...)
	return n, nil
}
