package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dgoings/workbook/internal/cli"
)

func main() {
	configureReleaseMetadata()
	// SIGTERM matters for the long-running commands. A backgrounded
	// `workbook sync --watch` is stopped with plain `kill` far more often than
	// with Ctrl-C, and an untrapped SIGTERM would skip the final sync that
	// publishes work the watcher was still holding.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], ".", os.Stdout, os.Stderr))
}
