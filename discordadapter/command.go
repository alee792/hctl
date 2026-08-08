package discordadapter

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"hctl/channeladapter"
)

// RunCommand implements only the four fixed modes declared by the package
// capability. Operation stdout is one closed non-secret JSON object; runtime
// stdout is protocol frames only.
func RunCommand(ctx context.Context, args []string, input io.Reader, output, terminal io.Writer, dependencies Dependencies) error {
	if len(args) == 0 {
		return errors.New("usage: hctl-discord <run --stdio|setup|status|remove>")
	}
	if args[0] == "run" {
		if len(args) != 2 || args[1] != "--stdio" {
			return errors.New("usage: hctl-discord run --stdio")
		}
		runtime, err := NewRuntime(output, dependencies)
		if err != nil {
			return err
		}
		return runtime.Run(ctx, input)
	}
	if args[0] != "setup" && args[0] != "status" && args[0] != "remove" {
		return errors.New("usage: hctl-discord <run --stdio|setup|status|remove>")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(terminal)
	profileID := flags.String("profile", "", "non-secret Discord profile selector")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !validProfileID(*profileID) {
		return fmt.Errorf("usage: hctl-discord %s --profile PROFILE", args[0])
	}
	var result channeladapter.OperationResult
	var err error
	switch args[0] {
	case "setup":
		result, err = setup(ctx, *profileID, input, terminal, dependencies)
	case "status":
		result, err = status(ctx, *profileID, dependencies)
	case "remove":
		result, err = remove(*profileID, dependencies)
	}
	if err != nil {
		return err
	}
	data, err := channeladapter.MarshalOperationResult(result)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = output.Write(data)
	return err
}
