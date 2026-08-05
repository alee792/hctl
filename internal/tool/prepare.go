package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"hctl/internal/rootfs"
)

//go:embed host/typescript.ts
var typescriptHost []byte

//go:embed host/python.py
var pythonHost []byte

//go:embed host/go-main.go.tmpl
var goHostTemplate []byte

const (
	goSchemaVersion   = "v0.14.0"
	goValidateVersion = "v6.0.2"
)

func Prepare(ctx context.Context, root, sourceFingerprint string, inventory Inventory) error {
	if len(inventory.Sources) == 0 {
		return nil
	}
	cache := cacheRelative(sourceFingerprint)
	if hasLanguage(inventory, TypeScript) {
		host := cache + "/typescript.ts"
		if err := rootfs.WriteAtomic(root, host, typescriptHost, 0o644); err != nil {
			return err
		}
		deno, err := executable("deno")
		if err != nil {
			return err
		}
		args := []string{"check", "--config", filepath.Join(root, "deno.json"), "--frozen", filepath.Join(root, filepath.FromSlash(host))}
		for _, file := range inventory.Files {
			if filepath.Ext(file.Path) == ".ts" {
				args = append(args, filepath.Join(root, filepath.FromSlash(file.Path)))
			}
		}
		if err := runNative(ctx, root, "Deno tool check", deno, args...); err != nil {
			return err
		}
	}
	if hasLanguage(inventory, Python) {
		host := cache + "/python.py"
		if err := rootfs.WriteAtomic(root, host, pythonHost, 0o644); err != nil {
			return err
		}
		uv, err := executable("uv")
		if err != nil {
			return err
		}
		if err := runNative(ctx, root, "Python tool sync", uv, "sync", "--locked", "--project", root); err != nil {
			return err
		}
	}
	if hasLanguage(inventory, Go) {
		if _, err := prepareGo(ctx, root, sourceFingerprint, inventory); err != nil {
			return err
		}
	}
	runtime, err := Open(ctx, root, sourceFingerprint, inventory)
	if err != nil {
		return err
	}
	runtime.Close()
	return nil
}

func prepareGo(ctx context.Context, root, sourceFingerprint string, inventory Inventory) (string, error) {
	cache := cacheRelative(sourceFingerprint) + "/go"
	binary := filepath.Join(root, filepath.FromSlash(cache+"/host"))
	if info, err := os.Lstat(binary); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return "", errors.New("cached Go tool host must be a regular executable")
		}
		return binary, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect cached Go tool host")
	}

	goExecutable, err := executable("go")
	if err != nil {
		return "", err
	}
	moduleBytes, err := runNativeOutput(ctx, root, "Go module inspection", goExecutable, "list", "-m", "-f={{.Path}}")
	if err != nil {
		return "", err
	}
	module := strings.TrimSpace(string(moduleBytes))
	if module == "" || strings.ContainsAny(module, " \t\r\n") {
		return "", errors.New("go tool module path is invalid")
	}

	var imports, registrations []string
	for _, source := range inventory.Sources {
		if source.Language != Go {
			continue
		}
		directory := filepath.ToSlash(filepath.Dir(source.Path))
		alias := fmt.Sprintf("tool%d", len(imports))
		imports = append(imports, fmt.Sprintf("\t%s %q", alias, module+"/"+directory))
		registrations = append(registrations, fmt.Sprintf("\tnewTool[%s.Input, %s.Output](%q, %s.Description, %s.Execute),", alias, alias, source.Name, alias, alias))
	}
	sort.Strings(imports)
	mainSource := strings.ReplaceAll(string(goHostTemplate), "{{IMPORTS}}", strings.Join(imports, "\n"))
	mainSource = strings.ReplaceAll(mainSource, "{{TOOLS}}", strings.Join(registrations, "\n"))
	goMod := fmt.Sprintf("module hctl.local/tool-host\n\ngo 1.24.0\n\nrequire (\n\tgithub.com/invopop/jsonschema %s\n\tgithub.com/santhosh-tekuri/jsonschema/v6 %s\n\t%s %s\n)\n\nreplace %s => %s\n", goSchemaVersion, goValidateVersion, module, localModuleVersion(module), module, strconv.Quote(root))
	if err := rootfs.WriteAtomic(root, cache+"/main.go", []byte(mainSource), 0o644); err != nil {
		return "", err
	}
	if err := rootfs.WriteAtomic(root, cache+"/go.mod", []byte(goMod), 0o644); err != nil {
		return "", err
	}
	cacheDirectory := filepath.Join(root, filepath.FromSlash(cache))
	if info, err := os.Lstat(filepath.Join(cacheDirectory, "go.sum")); err == nil && !info.Mode().IsRegular() {
		return "", errors.New("cached Go tool go.sum must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect cached Go tool go.sum")
	}
	if err := runNative(ctx, cacheDirectory, "Go tool dependency sync", goExecutable, "mod", "tidy"); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(cacheDirectory, ".host-*")
	if err != nil {
		return "", errors.New("cannot stage Go tool host")
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return "", errors.New("cannot stage Go tool host")
	}
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := runNative(ctx, cacheDirectory, "Go tool build", goExecutable, "build", "-mod=readonly", "-o", temporaryPath, "."); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return "", errors.New("cannot make Go tool host executable")
	}
	if err := os.Rename(temporaryPath, binary); err != nil {
		return "", errors.New("cannot install Go tool host")
	}
	return binary, nil
}

func cacheRelative(sourceFingerprint string) string {
	digest := sha256.Sum256(bytes.Join([][]byte{typescriptHost, pythonHost, goHostTemplate}, nil))
	return ".hctl/cache/tools/" + sourceFingerprint + "-" + hex.EncodeToString(digest[:6])
}

func localModuleVersion(module string) string {
	last := pathpkg.Base(module)
	major := strings.TrimPrefix(last, "v")
	if dot := strings.LastIndex(last, ".v"); dot >= 0 {
		major = last[dot+2:]
	}
	if number, err := strconv.Atoi(major); err == nil && number >= 2 {
		return fmt.Sprintf("v%d.0.0", number)
	}
	return "v0.0.0"
}

func executable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is required for authored tools", name)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s executable", name)
	}
	return path, nil
}

func runNative(ctx context.Context, directory, label, executable string, arguments ...string) error {
	_, err := runNativeOutput(ctx, directory, label, executable, arguments...)
	return err
}

func runNativeOutput(ctx context.Context, directory, label, executable string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	output := &boundedBuffer{remaining: 64 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(string(output.Bytes()))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("%s failed: %s", label, detail)
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > buffer.remaining {
		data = data[:max(buffer.remaining, 0)]
	}
	buffer.remaining -= len(data)
	_, _ = buffer.buffer.Write(data)
	return original, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return append([]byte(nil), buffer.buffer.Bytes()...) }
