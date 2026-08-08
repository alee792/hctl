package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hctl/discordadapter"
)

func main() {
	dependencies, err := discordadapter.DefaultDependencies()
	if err != nil {
		fatal(err)
	}
	// Interactive setup is the foreground terminal process. Keep the default
	// SIGINT/SIGTERM disposition so terminal cancellation interrupts a blocked
	// password or scope read instead of only cancelling a context it cannot yet
	// observe.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := discordadapter.RunCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, dependencies); err != nil {
			fatal(err)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := discordadapter.RunCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, dependencies); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "hctl-discord:", err)
	os.Exit(1)
}
