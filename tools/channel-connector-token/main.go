package main

import (
	"fmt"
	"os"

	"github.com/omnara-ai/omnara/internal/bearertoken"
)

func main() {
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, token)
}
