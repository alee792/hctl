package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenRootDependencies = []string{
	"github.com/bwmarrin/discordgo",
	"github.com/gorilla/websocket",
	"github.com/zalando/go-keyring",
	"hctl/discordadapter",
}

func TestRootBinaryDependencyBoundaryExcludesDiscordImplementation(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoForbiddenRootDependency(t, "root go.mod", string(module))

	list := exec.Command("go", "list", "-mod=readonly", "-deps", "-f={{.ImportPath}}", "./cmd/hctl")
	list.Dir = repository
	list.Env = append(os.Environ(), "GOWORK=off")
	imports, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("list root production dependency graph: %v\n%s", err, imports)
	}
	assertNoForbiddenRootDependency(t, "root production imports", string(imports))

	tests := exec.Command("go", "list", "-mod=readonly", "-test", "-deps", "-f={{.ImportPath}}", "./...")
	tests.Dir = repository
	tests.Env = append(os.Environ(), "GOWORK=off")
	testImports, err := tests.CombinedOutput()
	if err != nil {
		t.Fatalf("list ordinary root test dependency graph: %v\n%s", err, testImports)
	}
	assertNoForbiddenRootDependency(t, "ordinary root test imports", string(testImports))

	binary := filepath.Join(t.TempDir(), "hctl")
	build := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-o", binary, "./cmd/hctl")
	build.Dir = repository
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build root hctl binary: %v\n%s", err, output)
	}
	metadata := exec.Command("go", "version", "-m", binary)
	output, err := metadata.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect root hctl binary dependencies: %v\n%s", err, output)
	}
	assertNoForbiddenRootDependency(t, "root binary metadata", string(output))
}

func assertNoForbiddenRootDependency(t *testing.T, surface, content string) {
	t.Helper()
	for _, dependency := range forbiddenRootDependencies {
		if strings.Contains(content, dependency) {
			t.Fatalf("%s contains forbidden Discord implementation dependency %q", surface, dependency)
		}
	}
}
