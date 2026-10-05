package main

import (
	"fmt"
	"os"

	"github.com/omnara-ai/omnara/internal/sandbox"
)

func main() {
	if err := sandbox.Run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
