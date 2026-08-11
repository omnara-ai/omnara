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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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
	case "compare":
		if len(args) != 2 {
			return usage(stderr)
		}
		commandErr = compareRepository(root, args[1])
	case "update":
		if len(args) != 1 {
			return usage(stderr)
		}
		commandErr = updateRepository(root)
		if commandErr == nil {
			_, commandErr = fmt.Fprintln(stdout, "migration checksum manifests updated")
		}
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
	return operationalFailure(stderr, "usage: migrationcheck check|update|compare <trusted-base-sha>")
}

func operationalFailure(stderr io.Writer, message string) int {
	_, _ = fmt.Fprintln(stderr, message)
	return 2
}
