package main

import (
	"fmt"
	"os"

	"github.com/omnara-ai/omnara/internal/fileexec"
)

func main() {
	if err := fileexec.RunEdit(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
