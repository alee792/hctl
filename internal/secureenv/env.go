package secureenv

import (
	"os"
	"sort"
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
	return WithValues(map[string]string{key: value})
}

// WithValues returns the scrubbed child environment with the selected values
// replaced in stable key order.
func WithValues(values map[string]string) []string {
	return Replace(Child(), values)
}

// Staging returns a deliberately small environment for credential-free build
// and inspection subprocesses. Toolchain discovery happens before this
// boundary, so only platform execution inputs are retained.
func Staging(home string) []string {
	allowed := map[string]bool{
		"GOROOT": true, "LANG": true, "LC_ALL": true, "PATH": true,
		"SSL_CERT_DIR": true, "SSL_CERT_FILE": true, "SYSTEMROOT": true,
		"TMPDIR": true,
	}
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] || strings.HasPrefix(key, "LC_") {
			result = append(result, entry)
		}
	}
	return Replace(result, map[string]string{"HOME": home})
}

// Replace updates an already selected environment in stable key order.
func Replace(environment []string, values map[string]string) []string {
	result := append([]string(nil), environment...)
	filtered := result[:0]
	for _, entry := range result {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			filtered = append(filtered, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		filtered = append(filtered, key+"="+values[key])
	}
	return filtered
}
