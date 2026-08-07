package secureenv

import (
	"os"
	"strings"
)

// Child returns the process environment without channel credentials.
func Child() []string {
	result := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !privateRuntimeEntry(entry) {
			result = append(result, entry)
		}
	}
	return result
}

func privateRuntimeEntry(entry string) bool {
	return strings.HasPrefix(entry, "HCTL_DISCORD_TOKEN=") ||
		strings.HasPrefix(entry, "HCTL_CLAUDE_DEFERRED_BROKER=")
}

func With(key, value string) []string {
	prefix := key + "="
	result := Child()
	filtered := result[:0]
	for _, entry := range result {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}
