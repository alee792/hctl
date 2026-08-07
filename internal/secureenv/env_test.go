package secureenv

import (
	"strings"
	"testing"
)

func TestChildRemovesDiscordToken(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "do-not-inherit")
	t.Setenv("HCTL_SAFE_TEST", "retained")
	t.Setenv("HCTL_CLAUDE_DEFERRED_BROKER", "private-broker")
	joined := strings.Join(Child(), "\n")
	if strings.Contains(joined, "do-not-inherit") || strings.Contains(joined, "private-broker") || !strings.Contains(joined, "HCTL_SAFE_TEST=retained") {
		t.Fatalf("sanitized environment = %q", joined)
	}
}
