package imageinput

import (
	"path/filepath"
	"testing"

	"hctl/internal/version"
)

func TestCheckedInInputsAreValid(t *testing.T) {
	inputs, err := Load(filepath.Join("..", "..", "images", "inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := inputs.Components["claude"].PublicationGate; got != "blocked-pending-permission" {
		t.Fatalf("Claude publication gate = %q", got)
	}
	if got := inputs.Target.Base.Digest; got == "" {
		t.Fatal("compatible base is not pinned")
	}
	if got := inputs.HCTL.DevelopmentVersion; got != version.Value {
		t.Fatalf("development image version = %q, source version = %q", got, version.Value)
	}
}

func TestRejectsOpenClaudePublicationGate(t *testing.T) {
	inputs, err := Load(filepath.Join("..", "..", "images", "inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	component := inputs.Components["claude"]
	component.PublicationGate = "open"
	inputs.Components["claude"] = component
	if err := inputs.Validate(); err == nil {
		t.Fatal("open Claude publication gate was accepted")
	}
}
