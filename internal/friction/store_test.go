package friction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/project"
)

func TestStoreRecordsPrivateRuntimeProvenance(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	store.now = func() time.Time { return time.Date(2026, 8, 8, 12, 34, 56, 123000000, time.FixedZone("local", 3600)) }
	store.random = &repeatingReader{value: 0xab}
	p := testProject("reviewer@0123456789ab")
	if !store.Record(p, "The tool contract required three attempts to interpret.") {
		t.Fatal("note was not recorded")
	}
	directory := filepath.Join(root, "friction", "agents", p.AgentID)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != ".lock" {
		t.Fatalf("state entries = %#v", entries)
	}
	data, err := os.ReadFile(filepath.Join(directory, entries[1].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var got entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.ID != "20260808T113456.123000000Z-abababababababab" || !got.CreatedAt.Equal(time.Date(2026, 8, 8, 11, 34, 56, 123000000, time.UTC)) || got.Agent.ID != p.AgentID || got.Agent.Name != p.Name || got.Agent.SourceFingerprint != p.SourceFingerprint || got.Runtime.HCTLVersion != project.GeneratorVersion || got.Runtime.Harness != "codex" || got.Note == "" {
		t.Fatalf("record = %#v", got)
	}
	for _, path := range []string{directory, filepath.Join(directory, ".lock"), filepath.Join(directory, entries[1].Name())} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
}

func TestStoreBoundsAndNamespacesRecords(t *testing.T) {
	root := t.TempDir()
	first := NewStore(root)
	second := NewStore(root)
	p1 := testProject("reviewer@0123456789ab")
	p2 := testProject("reviewer@abcdef012345")
	if !first.Record(p1, "first") || !second.Record(p2, "second") {
		t.Fatal("namespaced notes were not recorded")
	}
	for _, p := range []*project.Project{p1, p2} {
		matches, err := filepath.Glob(filepath.Join(root, "friction", "agents", p.AgentID, "*.json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("agent %s records = %v, error = %v", p.AgentID, matches, err)
		}
	}
	if first.Record(p1, "") || first.Record(p1, string(make([]byte, MaxNoteBytes+1))) {
		t.Fatal("invalid note was recorded")
	}
}

func TestStoreCapacityAndConcurrentPublicationAreBounded(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	p := testProject("reviewer@0123456789ab")
	var group sync.WaitGroup
	for index := 0; index < MaxRecords+32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			store.Record(p, "concurrent friction")
		}()
	}
	group.Wait()
	matches, err := filepath.Glob(filepath.Join(root, "friction", "agents", p.AgentID, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 || len(matches) > MaxRecords {
		t.Fatalf("record count = %d", len(matches))
	}
}

func TestStoreUnsafeOrFullStateNoops(t *testing.T) {
	t.Run("missing explicit root", func(t *testing.T) {
		if NewStore(filepath.Join(t.TempDir(), "missing")).Record(testProject("reviewer@0123456789ab"), "friction") {
			t.Fatal("missing explicit root accepted a record")
		}
	})
	t.Run("symlinked agent directory", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "friction", "agents")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		p := testProject("reviewer@0123456789ab")
		if err := os.Symlink(t.TempDir(), filepath.Join(parent, p.AgentID)); err != nil {
			t.Fatal(err)
		}
		if NewStore(root).Record(p, "friction") {
			t.Fatal("symlinked state accepted a record")
		}
	})
	t.Run("full", func(t *testing.T) {
		root := t.TempDir()
		p := testProject("reviewer@0123456789ab")
		directory := filepath.Join(root, "friction", "agents", p.AgentID)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < MaxRecords; index++ {
			path := filepath.Join(directory, time.Unix(int64(index), 0).UTC().Format("20060102T150405.000000000Z")+"-0000000000000000.json")
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if NewStore(root).Record(p, "friction") {
			t.Fatal("full state accepted a record")
		}
	})
	t.Run("colliding identifier", func(t *testing.T) {
		root := t.TempDir()
		p := testProject("reviewer@0123456789ab")
		store := NewStore(root)
		store.now = func() time.Time { return time.Unix(1, 0) }
		store.random = &repeatingReader{value: 0xab}
		if !store.Record(p, "first") || store.Record(p, "second") {
			t.Fatal("identifier collision did not preserve the first record")
		}
		matches, err := filepath.Glob(filepath.Join(root, "friction", "agents", p.AgentID, "*.json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("colliding records = %v, error = %v", matches, err)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"note": "first"`) || strings.Contains(string(data), "second") {
			t.Fatalf("first record was replaced: %s", data)
		}
	})
	t.Run("random source unavailable", func(t *testing.T) {
		store := NewStore(t.TempDir())
		store.random = failingReader{}
		if store.Record(testProject("reviewer@0123456789ab"), "friction") {
			t.Fatal("failed random source accepted a record")
		}
	})
}

func TestDefaultStatePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		goos, xdg, suffix string
	}{
		{goos: "darwin", suffix: filepath.Join("hctl", "state")},
		{goos: "linux", suffix: filepath.Join(".local", "state", "hctl")},
		{goos: "linux", xdg: filepath.Join(home, "state"), suffix: filepath.Join("state", "hctl")},
	} {
		base, prefix, err := defaultStateBase(test.goos, test.xdg)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Join(base, prefix) != filepath.Join(home, test.suffix) && test.goos != "darwin" {
			t.Fatalf("%s state path = %s", test.goos, filepath.Join(base, prefix))
		}
		if test.goos == "darwin" && filepath.Base(filepath.Join(base, prefix)) != "state" {
			t.Fatalf("darwin state path = %s", filepath.Join(base, prefix))
		}
	}
}

func testProject(id string) *project.Project {
	return &project.Project{AgentID: id, Name: "reviewer", SourceFingerprint: strings.Repeat("ab", 32), Harness: "codex"}
}

type repeatingReader struct{ value byte }

func (reader *repeatingReader) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = reader.value
	}
	return len(data), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, os.ErrPermission
}
