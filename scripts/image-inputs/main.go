package main

import (
	"errors"
	"fmt"
	"os"

	"hctl/internal/imageinput"
	"hctl/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "image-inputs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[0] == "check" {
		_, err := imageinput.Load(args[1])
		return err
	}
	if len(args) == 2 && args[0] == "development-version" {
		inputs, err := imageinput.Load(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, inputs.HCTL.DevelopmentVersion)
		return err
	}
	if len(args) == 2 && args[0] == "validate-version" {
		return version.Validate(args[1])
	}
	if len(args) == 4 && args[0] == "fetch" {
		inputs, err := imageinput.Load(args[1])
		if err != nil {
			return err
		}
		return imageinput.Fetch(inputs, args[2], args[3])
	}
	return errors.New("usage: image-inputs check FILE | image-inputs development-version FILE | image-inputs validate-version VERSION | image-inputs fetch FILE COMPONENT OUTPUT")
}
