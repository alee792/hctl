package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hctl/internal/rootfs"
)

const (
	maxFiles       = 128
	maxTools       = 32
	maxFileBytes   = 1 << 20
	maxSourceBytes = 8 << 20
)

var portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Language string

const (
	TypeScript Language = "typescript"
	Python     Language = "python"
	Go         Language = "go"
)

type Source struct {
	Name     string
	Language Language
	Path     string
}

type File struct {
	Path   string
	SHA256 string
}

type Inventory struct {
	Sources []Source
	Files   []File
}

func Discover(root string) (Inventory, error) {
	directory := filepath.Join(root, "tools")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return Inventory{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Inventory{}, errors.New("tools must be a real directory")
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return Inventory{}, errors.New("cannot read tools directory")
	}
	inventory := Inventory{}
	seen := map[string]string{}
	total := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "__pycache__" {
			continue
		}
		if entry.IsDir() {
			name := strings.ReplaceAll(entry.Name(), "_", "-")
			if !portableName.MatchString(name) {
				return Inventory{}, fmt.Errorf("go tool directory %q must use lowercase letters, numbers, underscores, or hyphens", entry.Name())
			}
			toolPath := "tools/" + entry.Name() + "/tool.go"
			if _, err := rootfs.ReadSource(root, toolPath, maxFileBytes); err != nil {
				return Inventory{}, fmt.Errorf("go tool %q: %w", name, err)
			}
			if err := addSource(&inventory, seen, Source{Name: name, Language: Go, Path: toolPath}); err != nil {
				return Inventory{}, err
			}
			directoryFiles, err := os.ReadDir(filepath.Join(directory, entry.Name()))
			if err != nil {
				return Inventory{}, fmt.Errorf("cannot read Go tool directory %q", entry.Name())
			}
			for _, child := range directoryFiles {
				if strings.HasPrefix(child.Name(), ".") {
					continue
				}
				if child.IsDir() {
					return Inventory{}, fmt.Errorf("go tool directory %q may not contain nested directories", entry.Name())
				}
				path := "tools/" + entry.Name() + "/" + child.Name()
				if err := addFile(root, path, &inventory, &total); err != nil {
					return Inventory{}, err
				}
			}
			continue
		}

		extension := filepath.Ext(entry.Name())
		language := Language("")
		switch extension {
		case ".ts":
			language = TypeScript
		case ".py":
			language = Python
		default:
			return Inventory{}, fmt.Errorf("tools may contain TypeScript, Python, or Go tool directories; found %q", entry.Name())
		}
		path := "tools/" + entry.Name()
		if err := addFile(root, path, &inventory, &total); err != nil {
			return Inventory{}, err
		}
		if strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		name := strings.ReplaceAll(strings.TrimSuffix(entry.Name(), extension), "_", "-")
		if !portableName.MatchString(name) {
			return Inventory{}, fmt.Errorf("tool filename %q must use lowercase letters, numbers, underscores, or hyphens", entry.Name())
		}
		if err := addSource(&inventory, seen, Source{Name: name, Language: language, Path: path}); err != nil {
			return Inventory{}, err
		}
	}

	if len(inventory.Sources) == 0 {
		if len(inventory.Files) == 0 {
			return Inventory{}, nil
		}
		return Inventory{}, errors.New("tools directory contains no tool files")
	}
	if hasLanguage(inventory, TypeScript) {
		for _, path := range []string{"deno.json", "deno.lock"} {
			if err := addFile(root, path, &inventory, &total); err != nil {
				return Inventory{}, fmt.Errorf("TypeScript tools require %s: %w", path, err)
			}
		}
	}
	if hasLanguage(inventory, Python) {
		for _, path := range []string{"pyproject.toml", "uv.lock"} {
			if err := addFile(root, path, &inventory, &total); err != nil {
				return Inventory{}, fmt.Errorf("python tools require %s: %w", path, err)
			}
		}
	}
	if hasLanguage(inventory, Go) {
		if err := addFile(root, "go.mod", &inventory, &total); err != nil {
			return Inventory{}, fmt.Errorf("go tools require go.mod: %w", err)
		}
		if _, _, exists, err := rootfs.ReadOptional(root, "go.sum", maxFileBytes); err != nil {
			return Inventory{}, err
		} else if exists {
			if err := addFile(root, "go.sum", &inventory, &total); err != nil {
				return Inventory{}, err
			}
		}
	}

	sort.Slice(inventory.Sources, func(i, j int) bool { return inventory.Sources[i].Name < inventory.Sources[j].Name })
	sort.Slice(inventory.Files, func(i, j int) bool { return inventory.Files[i].Path < inventory.Files[j].Path })
	return inventory, nil
}

func addSource(inventory *Inventory, seen map[string]string, source Source) error {
	if source.Name == "echo" {
		return errors.New("tool name \"echo\" is reserved by hctl")
	}
	if prior := seen[source.Name]; prior != "" {
		return fmt.Errorf("duplicate tool name %q from %s and %s", source.Name, prior, source.Path)
	}
	if len(inventory.Sources) == maxTools {
		return fmt.Errorf("agent may contain at most %d tools", maxTools)
	}
	seen[source.Name] = source.Path
	inventory.Sources = append(inventory.Sources, source)
	return nil
}

func addFile(root, path string, inventory *Inventory, total *int) error {
	if len(inventory.Files) == maxFiles {
		return fmt.Errorf("tool source may contain at most %d files", maxFiles)
	}
	data, err := rootfs.ReadSource(root, path, maxFileBytes)
	if err != nil {
		return err
	}
	*total += len(data)
	if *total > maxSourceBytes {
		return fmt.Errorf("tool source exceeds %d bytes", maxSourceBytes)
	}
	inventory.Files = append(inventory.Files, File{Path: path, SHA256: rootfs.SHA256(data)})
	return nil
}

func hasLanguage(inventory Inventory, language Language) bool {
	for _, source := range inventory.Sources {
		if source.Language == language {
			return true
		}
	}
	return false
}
