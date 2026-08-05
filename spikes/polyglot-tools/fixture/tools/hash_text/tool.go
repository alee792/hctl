package hashtext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

const Description = "Hash bounded text with SHA-256."

var calls int

type Input struct {
	Text string `json:"text" jsonschema:"minLength=1,maxLength=1024"`
}

type Output struct {
	SHA256 string `json:"sha256" jsonschema:"minLength=64,maxLength=64"`
	Calls  int    `json:"calls" jsonschema:"minimum=1"`
}

func Execute(_ context.Context, input Input) (Output, error) {
	calls++
	digest := sha256.Sum256([]byte(input.Text))
	return Output{SHA256: hex.EncodeToString(digest[:]), Calls: calls}, nil
}
