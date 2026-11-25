//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"strconv"
)

func newFileSearchCommand(
	ctx context.Context, binary string, args []string, roots []*os.File, storeCount int,
) (*exec.Cmd, error) {
	command := exec.CommandContext(ctx, "omnara-sandbox", append([]string{strconv.Itoa(storeCount), binary}, args...)...)
	command.ExtraFiles = roots
	return command, nil
}

func newScriptEditCommand(ctx context.Context, script string) (*exec.Cmd, func(), error) {
	command := exec.CommandContext(ctx, "omnara-chroot-sandbox", script)
	command.Env = []string{}
	return command, func() {}, nil
}

func scriptEditDiagnostic(_ *exec.Cmd, diagnostic string) string { return diagnostic }
