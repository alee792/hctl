package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubImageCheckScriptParsesDirectAndStagedArguments(t *testing.T) {
	script := filepath.Join("..", "images", "codex", "acceptance", "check-github-image.sh")
	const marker = "local-fake-github-marker-must-not-persist"
	payload := []byte("fake external github server\n")
	digest := sha256.Sum256(payload)
	expectedSHA := hex.EncodeToString(digest[:])

	for _, mode := range []string{"direct", "staged"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "workspace", ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			var executable string
			if mode == "direct" {
				executable = filepath.Join(root, "home", "hctl", ".config", "hctl", "integrations", "prepared", "exact-identity", "github-mcp-server")
				if err := os.MkdirAll(filepath.Join(root, "agent"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				executable = filepath.Join(root, "opt", "hctl", "integrations", "github-mcp-server", "manifest-identity", "linux-amd64", "github-mcp-server")
			}
			if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, payload, 0o700); err != nil {
				t.Fatal(err)
			}
			config := strings.Join([]string{
				`[mcp_servers."github"]`,
				`command = "` + executable + `"`,
				`args = ["stdio"]`,
				`cwd = "` + filepath.Dir(executable) + `"`,
				`env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]`,
			}, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(root, "workspace", ".codex", "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			command := exec.Command("/bin/sh", script, mode, root)
			command.Env = append(os.Environ(), "GITHUB_PERSONAL_ACCESS_TOKEN="+marker, "EXPECTED_GITHUB_SHA256="+expectedSHA)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s image check = %s, %v", mode, output, err)
			}
		})
	}

	command := exec.Command("/bin/sh", script, "direct")
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "usage: check-github-image.sh") {
		t.Fatalf("invalid arguments = %s, %v", output, err)
	}
}
