package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return operationalFailure(stderr, "usage: sqlrules <query-directory>")
	}

	issues, err := checkDirectory(args[0])
	if err != nil {
		return operationalFailure(stderr, err.Error())
	}
	for _, issue := range issues {
		_, err := fmt.Fprintf(
			stdout,
			"%s:%d:%d: %s: %s\n",
			issue.Path,
			issue.Line,
			issue.Column,
			issue.Rule,
			issue.Message,
		)
		if err != nil {
			return operationalFailure(stderr, fmt.Sprintf("write diagnostic: %v", err))
		}
	}
	if len(issues) != 0 {
		return 1
	}
	return 0
}

func operationalFailure(stderr io.Writer, message string) int {
	_, _ = fmt.Fprintln(stderr, message)
	return 2
}
