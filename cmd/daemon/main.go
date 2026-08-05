package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/omnarad"
)

func main() {
	log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	if code := omnarad.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, log); code != 0 {
		os.Exit(code)
	}
}
