package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/mcpregistry"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-registry-sync:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mcp-registry-sync", flag.ContinueOnError)
	out := flags.String("out", "", "path of the snapshot file to write")
	upstream := flags.String("upstream", mcpregistry.DefaultUpstreamURL, "MCP registry base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := time.Now()
	syncer := mcpregistry.Syncer{
		UpstreamURL: *upstream,
		HTTPClient:  &http.Client{Timeout: 2 * time.Minute},
	}
	servers, err := syncer.Fetch(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		os.Stderr,
		"fetched %d servers from %s in %s\n",
		len(servers), *upstream, time.Since(started).Round(time.Millisecond),
	)
	snapshot := mcpregistry.BuildSnapshot(servers, time.Now())
	if err := mcpregistry.WriteSnapshot(path, snapshot); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d servers, %.1f MB)\n", path, len(snapshot.Servers), float64(info.Size())/1e6)
	return nil
}
