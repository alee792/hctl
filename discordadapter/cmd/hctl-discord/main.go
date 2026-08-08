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
