package main

import (
	"fmt"
	"os"

	"hctl/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ""); err != nil {
		fmt.Fprintln(os.Stderr, "hctl:", err)
		os.Exit(1)
	}
}
