package runtimeprobe

import "context"

const Description = "Report the Go authored-tool runtime."

type Input struct{}

type Output struct {
	Runtime string `json:"runtime"`
}

func Execute(context.Context, Input) (Output, error) {
	return Output{Runtime: "go"}, nil
}
