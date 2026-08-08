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
	// Setup and remove are foreground terminal operations. Keep the default
	// SIGINT/SIGTERM disposition so terminal cancellation interrupts a blocked
	// prompt, profile, or credential-store call instead of only cancelling a
	// context that operation code cannot yet observe.
	if usesForegroundTerminal(os.Args) {
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

func usesForegroundTerminal(args []string) bool {
	return len(args) > 1 && (args[1] == "setup" || args[1] == "remove")
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "hctl-discord:", err)
	os.Exit(1)
}
