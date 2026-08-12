package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	root, err := repositoryRoot()
	if err != nil {
		return operationalFailure(stderr, fmt.Sprintf("resolve repository root: %v", err))
	}
	var commandErr error
	switch args[0] {
	case "check":
		if len(args) != 1 {
			return usage(stderr)
		}
		commandErr = checkRepository(root)
	case "compare-releases":
		releaseRefRoot := "refs/tags"
		if len(args) == 3 && args[1] == "--release-ref-root" {
			releaseRefRoot = args[2]
		} else if len(args) != 1 {
			return usage(stderr)
		}
		commandErr = compareReleasedRepository(root, releaseRefRoot)
	default:
		return usage(stderr)
	}
	if commandErr != nil {
		return operationalFailure(stderr, commandErr.Error())
	}
	return 0
}

func repositoryRoot() (string, error) {
	output, err := exec.CommandContext(context.Background(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func usage(stderr io.Writer) int {
	return operationalFailure(
		stderr,
		"usage: migrationcheck check|compare-releases [--release-ref-root REF_ROOT]",
	)
}

func operationalFailure(stderr io.Writer, message string) int {
	_, _ = fmt.Fprintln(stderr, message)
	return 2
}
