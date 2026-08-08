package secureenv

import (
	"strings"
	"testing"
)

func TestChildRemovesDiscordToken(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "do-not-inherit")
	t.Setenv("HCTL_CHANNEL_ADAPTER_DESCRIPTOR", "/opt/hctl/integrations/channel-adapter.json")
	t.Setenv("HCTL_SAFE_TEST", "retained")
	t.Setenv("HCTL_CLAUDE_DEFERRED_BROKER", "private-broker")
	joined := strings.Join(Child(), "\n")
	if strings.Contains(joined, "do-not-inherit") || strings.Contains(joined, "CHANNEL_ADAPTER_DESCRIPTOR") || strings.Contains(joined, "private-broker") || !strings.Contains(joined, "HCTL_SAFE_TEST=retained") {
		t.Fatalf("sanitized environment = %q", joined)
	}
}

func TestStagingUsesCredentialFreeAllowlist(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv("OPENAI_API_KEY", "must-not-reach-stage")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-stage")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-reach-stage")
	t.Setenv("HCTL_DISCORD_TOKEN", "must-not-reach-stage")
	joined := strings.Join(Staging("/isolated/home"), "\n")
	for _, secret := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "HCTL_DISCORD_TOKEN", "must-not-reach-stage"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("staging environment contains %q: %s", secret, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/safe/bin") || !strings.Contains(joined, "HOME=/isolated/home") {
		t.Fatalf("staging environment lacks execution inputs: %s", joined)
	}
}
