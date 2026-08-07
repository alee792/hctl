// Package imageinput validates and fetches pinned harness-image inputs.
package imageinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"hctl/internal/version"
)

const maxComponentBytes = int64(512 << 20)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	baseReference   = regexp.MustCompile(`^docker\.io/library/[a-z0-9]+(?:[._-][a-z0-9]+)*:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

type Inputs struct {
	SchemaVersion int                  `json:"schema_version"`
	Target        Target               `json:"target"`
	HCTL          HCTL                 `json:"hctl"`
	Components    map[string]Component `json:"components"`
}

type Target struct {
	OS             string          `json:"os"`
	Architecture   string          `json:"architecture"`
	ABI            string          `json:"abi"`
	CompatibleBase string          `json:"compatible_base"`
	Base           Base            `json:"base"`
	Runtime        RuntimeContract `json:"runtime"`
}

type Base struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type RuntimeContract struct {
	UID                 int      `json:"uid"`
	GID                 int      `json:"gid"`
	Home                string   `json:"home"`
	RequiredExecutables []string `json:"required_executables"`
	CertificateBundle   string   `json:"certificate_bundle"`
	WritablePaths       []string `json:"writable_paths"`
}

type HCTL struct {
	DevelopmentVersion string `json:"development_version"`
}

type Component struct {
	Version         string `json:"version"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	Format          string `json:"format"`
	License         string `json:"license"`
	PublicationGate string `json:"publication_gate"`
}

func Load(path string) (Inputs, error) {
	file, err := os.Open(path)
	if err != nil {
		return Inputs{}, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var inputs Inputs
	if err := decoder.Decode(&inputs); err != nil {
		return Inputs{}, fmt.Errorf("decode image inputs: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Inputs{}, errors.New("decode image inputs: expected one JSON document")
	}
	if err := inputs.Validate(); err != nil {
		return Inputs{}, err
	}
	return inputs, nil
}

func (inputs Inputs) Validate() error {
	if inputs.SchemaVersion != 1 {
		return errors.New("image inputs schema_version must be 1")
	}
	if inputs.Target.OS != "linux" || inputs.Target.Architecture != "amd64" || inputs.Target.ABI != "glibc" || inputs.Target.CompatibleBase != "linux-amd64-glibc" {
		return errors.New("image inputs target must be linux/amd64 with the glibc compatible-base contract")
	}
	if !baseReference.MatchString(inputs.Target.Base.Reference) || !digestPattern.MatchString(inputs.Target.Base.Digest) {
		return errors.New("image inputs base must be an official image reference pinned by a sha256 platform digest")
	}
	runtime := inputs.Target.Runtime
	if runtime.UID != 65532 || runtime.GID != 65532 || runtime.Home != "/home/hctl" || runtime.CertificateBundle != "/etc/ssl/certs/ca-certificates.crt" {
		return errors.New("image inputs runtime contract is invalid")
	}
	if strings.Join(runtime.RequiredExecutables, "\n") != "/bin/sh\n/usr/bin/id" || strings.Join(runtime.WritablePaths, "\n") != "/home/hctl\n/workspace" {
		return errors.New("image inputs runtime paths must be canonical and ordered")
	}
	if version.Validate(inputs.HCTL.DevelopmentVersion) != nil {
		return errors.New("hctl development version must be an exact semantic version")
	}
	want := []string{"claude", "codex", "deno", "go", "python", "uv"}
	got := make([]string, 0, len(inputs.Components))
	for name := range inputs.Components {
		got = append(got, name)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("image input components must be exactly %s", strings.Join(want, ", "))
	}
	for _, name := range want {
		if err := validateComponent(name, inputs.Components[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateComponent(name string, component Component) error {
	if version.Validate(component.Version) != nil {
		return fmt.Errorf("image input %s version must be exact", name)
	}
	parsed, err := url.Parse(component.URL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !allowedHost(parsed.Hostname()) {
		return fmt.Errorf("image input %s URL is not an approved HTTPS source", name)
	}
	if !checksumPattern.MatchString(component.SHA256) {
		return fmt.Errorf("image input %s checksum must be a lowercase SHA-256", name)
	}
	if component.Size <= 0 || component.Size > maxComponentBytes {
		return fmt.Errorf("image input %s size is invalid", name)
	}
	if component.Format != "binary" && component.Format != "tar.gz" && component.Format != "zip" {
		return fmt.Errorf("image input %s format is invalid", name)
	}
	if component.License == "" {
		return fmt.Errorf("image input %s license is required", name)
	}
	wantGate := "open"
	if name == "claude" {
		wantGate = "blocked-pending-permission"
	}
	if component.PublicationGate != wantGate {
		return fmt.Errorf("image input %s publication gate must be %s", name, wantGate)
	}
	return nil
}

func allowedHost(host string) bool {
	switch host {
	case "downloads.claude.ai", "github.com", "go.dev":
		return true
	default:
		return false
	}
}

func allowedRedirectHost(host string) bool {
	return allowedHost(host) || host == "release-assets.githubusercontent.com" || host == "dl.google.com"
}

func Fetch(inputs Inputs, name, output string) error {
	component, ok := inputs.Components[name]
	if !ok {
		return fmt.Errorf("unknown image input component %q", name)
	}
	if output == "" {
		return errors.New("image input output path is required")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("image input output already exists or cannot be inspected")
	}
	parent, err := filepath.Abs(filepath.Dir(output))
	if err != nil {
		return errors.New("cannot resolve image input output directory")
	}
	temporary, err := os.CreateTemp(parent, ".hctl-image-input-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	client := &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 || request.URL.Scheme != "https" || !allowedRedirectHost(request.URL.Hostname()) {
				return errors.New("image input redirect left approved HTTPS sources")
			}
			return nil
		},
	}
	request, err := http.NewRequest(http.MethodGet, component.URL, nil)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_ = temporary.Close()
		return fmt.Errorf("fetch image input %s: unexpected HTTP status %s", name, response.Status)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, component.Size+1))
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written != component.Size {
		return fmt.Errorf("fetch image input %s: size %d does not match pinned size %d", name, written, component.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != component.SHA256 {
		return fmt.Errorf("fetch image input %s: SHA-256 does not match pin", name)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, output); err != nil {
		return err
	}
	return nil
}
