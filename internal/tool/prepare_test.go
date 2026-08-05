package tool

import "testing"

func TestLocalModuleVersionFollowsGoMajorPath(t *testing.T) {
	tests := map[string]string{
		"example.com/agent":    "v0.0.0",
		"example.com/agent/v2": "v2.0.0",
		"gopkg.in/agent.v3":    "v3.0.0",
	}
	for module, want := range tests {
		if got := localModuleVersion(module); got != want {
			t.Errorf("localModuleVersion(%q) = %q, want %q", module, got, want)
		}
	}
}
