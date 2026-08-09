// Package acquisition provides bounded, component-neutral acquisition and
// provenance for complete Agent Plugin and Agent Skill directories.
package acquisition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	LockFilename = "hctl-dependencies.json"
	maxLockBytes = 1 << 20
	maxEntries   = 384
)

var (
	pluginNamePattern = regexp.MustCompile(`^(?:[a-z0-9]|[a-z0-9](?:[a-z0-9.-]*[a-z0-9]))$`)
	skillNamePattern  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	hexSHA256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type Kind string

const (
	Plugin Kind = "plugin"
	Skill  Kind = "skill"
)

type SourceType string

const (
	SourceLocal   SourceType = "local"
	SourceGit     SourceType = "git"
	SourceArchive SourceType = "archive"
)

// Source is the closed provenance union recorded in hctl-dependencies.json.
// Fields not belonging to Type must be empty.
type Source struct {
	Type         SourceType `json:"type"`
	Path         string     `json:"path,omitempty"`
	URL          string     `json:"url,omitempty"`
	Ref          string     `json:"ref,omitempty"`
	Commit       string     `json:"commit,omitempty"`
	SHA256       string     `json:"sha256,omitempty"`
	Format       string     `json:"format,omitempty"`
	Subdirectory string     `json:"subdirectory,omitempty"`
}

type Dependency struct {
	Kind         Kind   `json:"kind"`
	Name         string `json:"name"`
	Destination  string `json:"destination"`
	Source       Source `json:"source"`
	MarkerSHA256 string `json:"marker_sha256"`
	TreeSHA256   string `json:"tree_sha256"`
	FileCount    uint64 `json:"file_count"`
	ByteCount    uint64 `json:"byte_count"`
}

type Lock struct {
	SchemaVersion int          `json:"schema_version"`
	Dependencies  []Dependency `json:"dependencies"`
}

func (source *Source) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("source must be an object")
	}
	var sourceType SourceType
	if raw, exists := fields["type"]; !exists || json.Unmarshal(raw, &sourceType) != nil {
		return errors.New("source type is required")
	}
	required := []string{"type"}
	optional := []string{"subdirectory"}
	switch sourceType {
	case SourceLocal:
		required = append(required, "path")
	case SourceGit:
		required = append(required, "url", "ref", "commit")
	case SourceArchive:
		required = append(required, "url", "sha256", "format")
	default:
		return errors.New("source type must be local, git, or archive")
	}
	if err := requireJSONKeys(fields, required, optional); err != nil {
		return err
	}
	type plain Source
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return errors.New("source fields have invalid types")
	}
	if _, exists := fields["subdirectory"]; exists && parsed.Subdirectory == "" {
		return errors.New("source subdirectory must be absent instead of empty")
	}
	*source = Source(parsed)
	return nil
}

func (dependency *Dependency) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("dependency must be an object")
	}
	if err := requireJSONKeys(fields, []string{"kind", "name", "destination", "source", "marker_sha256", "tree_sha256", "file_count", "byte_count"}, nil); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(fields["file_count"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(fields["byte_count"]), []byte("null")) {
		return errors.New("dependency counts must be nonnegative integers")
	}
	type plain Dependency
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return errors.New("dependency fields have invalid types")
	}
	*dependency = Dependency(parsed)
	return nil
}

func (lock *Lock) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("dependency lock must be an object")
	}
	if err := requireJSONKeys(fields, []string{"schema_version", "dependencies"}, nil); err != nil {
		return err
	}
	type plain Lock
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return errors.New("dependency lock fields have invalid types")
	}
	*lock = Lock(parsed)
	return nil
}

func requireJSONKeys(fields map[string]json.RawMessage, required, optional []string) error {
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, exists := fields[key]; !exists {
			return fmt.Errorf("required field %q is missing", key)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("field %q is not allowed", key)
		}
	}
	return nil
}

// ParseLock accepts key-order and whitespace variations but enforces the
// closed v1 schema, lexical dependency order, and every canonical value.
func ParseLock(data []byte) (Lock, error) {
	if len(data) == 0 || len(data) > maxLockBytes || !utf8.Valid(data) {
		return Lock{}, errors.New("hctl-dependencies.json must be bounded UTF-8 JSON")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Lock{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock Lock
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, errors.New("hctl-dependencies.json has an invalid closed schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Lock{}, errors.New("hctl-dependencies.json must contain one JSON value")
	}
	if err := ValidateLock(lock); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

// EncodeLock emits the exact deterministic v1 representation.
func EncodeLock(lock Lock) ([]byte, error) {
	if err := ValidateLock(lock); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(lock); err != nil || output.Len() > maxLockBytes {
		return nil, errors.New("cannot encode bounded hctl-dependencies.json")
	}
	return output.Bytes(), nil
}

func ValidateLock(lock Lock) error {
	if lock.SchemaVersion != 1 || lock.Dependencies == nil {
		return errors.New("hctl-dependencies.json requires schema_version 1 and a dependencies array")
	}
	if len(lock.Dependencies) > maxEntries {
		return fmt.Errorf("hctl-dependencies.json exceeds %d dependencies", maxEntries)
	}
	names := map[string]bool{}
	destinations := map[string]bool{}
	previous := ""
	for index, dependency := range lock.Dependencies {
		if err := validateDependency(dependency); err != nil {
			return fmt.Errorf("hctl-dependencies.json dependency %d: %w", index, err)
		}
		key := string(dependency.Kind) + "\x00" + dependency.Name
		if index > 0 && key <= previous {
			return errors.New("hctl-dependencies.json dependencies must be uniquely sorted by kind and name")
		}
		previous = key
		if names[key] || destinations[dependency.Destination] {
			return errors.New("hctl-dependencies.json contains a duplicate dependency or destination")
		}
		names[key], destinations[dependency.Destination] = true, true
	}
	return nil
}

func validateDependency(dependency Dependency) error {
	if dependency.Kind != Plugin && dependency.Kind != Skill {
		return errors.New("kind must be plugin or skill")
	}
	if !validName(dependency.Kind, dependency.Name) {
		return errors.New("name is invalid")
	}
	expected := string(dependency.Kind) + "s/" + dependency.Name
	if dependency.Destination != expected {
		return fmt.Errorf("destination must be %q", expected)
	}
	if !hexSHA256Pattern.MatchString(dependency.MarkerSHA256) || !hexSHA256Pattern.MatchString(dependency.TreeSHA256) {
		return errors.New("marker_sha256 and tree_sha256 must be lowercase SHA-256 values")
	}
	if dependency.FileCount > maxTreeFiles || dependency.ByteCount > maxTreeBytes {
		return errors.New("recorded counts exceed one dependency tree")
	}
	return validateSource(dependency.Source)
}

func validateSource(source Source) error {
	if source.Subdirectory != "" {
		if err := validateTreePath(source.Subdirectory); err != nil {
			return fmt.Errorf("source subdirectory: %w", err)
		}
	}
	switch source.Type {
	case SourceLocal:
		if source.Path == "" || len(source.Path) > 4096 || !utf8.ValidString(source.Path) || strings.ContainsAny(source.Path, "\\\x00") {
			return errors.New("local source path is invalid")
		}
		if source.URL != "" || source.Ref != "" || source.Commit != "" || source.SHA256 != "" || source.Format != "" {
			return errors.New("local source contains fields from another source type")
		}
		if err := validateLocalLocator(source.Path); err != nil {
			return err
		}
	case SourceGit:
		if err := validateHTTPSURL(source.URL); err != nil {
			return err
		}
		if len(source.Ref) < 1 || len(source.Ref) > 256 || !utf8.ValidString(source.Ref) || strings.HasPrefix(source.Ref, "-") || strings.ContainsRune(source.Ref, 0) {
			return errors.New("git source ref is invalid")
		}
		if !commitPattern.MatchString(source.Commit) {
			return errors.New("git source commit is invalid")
		}
		if source.Path != "" || source.SHA256 != "" || source.Format != "" {
			return errors.New("git source contains fields from another source type")
		}
	case SourceArchive:
		if err := validateHTTPSURL(source.URL); err != nil {
			return err
		}
		if !hexSHA256Pattern.MatchString(source.SHA256) || (source.Format != "zip" && source.Format != "tar.gz") {
			return errors.New("archive source digest or format is invalid")
		}
		if source.Path != "" || source.Ref != "" || source.Commit != "" {
			return errors.New("archive source contains fields from another source type")
		}
	default:
		return errors.New("source type must be local, git, or archive")
	}
	return nil
}

func validateHTTPSURL(value string) error {
	if len(value) == 0 || len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("source URL is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("source URL must be HTTPS without credentials, query, or fragment")
	}
	return nil
}

func validateLocalLocator(value string) error {
	if strings.HasPrefix(value, "/") || value == "." || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return errors.New("local source path must be a normalized relative slash path")
	}
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) != value {
		return errors.New("local source path must be normalized")
	}
	nonParent := false
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || len(part) > 255 {
			return errors.New("local source path is invalid")
		}
		if part == ".." {
			if nonParent {
				return errors.New("local source path permits parent components only at the beginning")
			}
			continue
		}
		nonParent = true
	}
	return nil
}

func validName(kind Kind, name string) bool {
	if len(name) == 0 || len(name) > 64 || !utf8.ValidString(name) {
		return false
	}
	if kind == Plugin {
		return pluginNamePattern.MatchString(name)
	}
	return skillNamePattern.MatchString(name)
}

func sortedDependencies(entries []Dependency) []Dependency {
	result := append([]Dependency(nil), entries...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var scan func() error
	scan = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("hctl-dependencies.json contains a duplicate object key")
				}
				seen[key] = true
				if err := scan(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := scan(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("hctl-dependencies.json has invalid JSON delimiters")
		}
	}
	if err := scan(); err != nil {
		return errors.New("hctl-dependencies.json has invalid or duplicate JSON keys")
	}
	return nil
}
