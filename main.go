package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/launchdarkly-labs/statsig-to-ld/cmd"
)

func main() {
	// Wire OS signals (Ctrl+C) into the context so in-flight HTTP requests
	// and retry sleeps are cancelled promptly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := cmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
