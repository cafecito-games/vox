// Command vox is a Discord CLI for humans and AI agents.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/cafecito-games/vox/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Execute(ctx)
}
