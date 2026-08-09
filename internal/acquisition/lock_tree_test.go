package acquisition

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockGoldenEncodingAndClosedParsing(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	lock := Lock{SchemaVersion: 1, Dependencies: []Dependency{{
		Kind: Skill, Name: "review", Destination: "skills/review",
		Source:       Source{Type: "local", Path: "../catalog/review"},
		MarkerSHA256: digestA, TreeSHA256: digestB, FileCount: 2, ByteCount: 17,
	}}}
	encoded, err := EncodeLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"schema_version\": 1,\n" +
		"  \"dependencies\": [\n" +
		"    {\n" +
		"      \"kind\": \"skill\",\n" +
		"      \"name\": \"review\",\n" +
		"      \"destination\": \"skills/review\",\n" +
		"      \"source\": {\n" +
		"        \"type\": \"local\",\n" +
		"        \"path\": \"../catalog/review\"\n" +
		"      },\n" +
		"      \"marker_sha256\": \"" + digestA + "\",\n" +
		"      \"tree_sha256\": \"" + digestB + "\",\n" +
		"      \"file_count\": 2,\n" +
		"      \"byte_count\": 17\n" +
		"    }\n" +
		"  ]\n" +
		"}\n"
	if string(encoded) != want {
		t.Fatalf("encoding mismatch\nwant:\n%s\ngot:\n%s", want, encoded)
	}
	parsed, err := ParseLock([]byte("{\"dependencies\":" + string(encoded[strings.Index(string(encoded), "["):strings.LastIndex(string(encoded), "]")+1]) + ",\"schema_version\":1}"))
	if err != nil || len(parsed.Dependencies) != 1 {
		t.Fatalf("key-order variation did not parse: %#v %v", parsed, err)
	}
	invalid := []string{
		`{"schema_version":1,"schema_version":1,"dependencies":[]}`,
		`{"schema_version":1,"dependencies":[],"extra":true}`,
		`{"schema_version":1,"dependencies":[{"kind":"skill","name":"review","destination":"skills/review","source":{"type":"local","path":"../catalog/review","subdirectory":""},"marker_sha256":"` + digestA + `","tree_sha256":"` + digestB + `","file_count":2,"byte_count":17}]}`,
		`{"schema_version":1,"dependencies":[{"kind":"skill","name":"review","destination":"skills/review","source":{"type":"local","path":"../catalog/review","url":""},"marker_sha256":"` + digestA + `","tree_sha256":"` + digestB + `","file_count":2,"byte_count":17}]}`,
		`{"schema_version":1,"dependencies":[{"kind":"skill","name":"review","destination":"skills/review","source":{"type":"local","path":"../catalog/review"},"marker_sha256":"` + digestA + `","tree_sha256":"` + digestB + `","file_count":null,"byte_count":17}]}`,
		`{"schema_version":1,"dependencies":[{"kind":"skill","name":"review","destination":"skills/review","source":{"type":"git","url":"https://:443/review.git","ref":"main","commit":"0123456789abcdef0123456789abcdef01234567"},"marker_sha256":"` + digestA + `","tree_sha256":"` + digestB + `","file_count":2,"byte_count":17}]}`,
	}
	for _, input := range invalid {
		if _, err := ParseLock([]byte(input)); err == nil {
			t.Fatalf("invalid closed lock parsed: %s", input)
		}
	}
}

func TestTreeGoldenIdentityAndUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tree, err := ReadTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if tree.SHA256 != "8cf513bc9041c1572b1d0876ac4c8e8d57c41e17d025f460bbce278d5eae1be7" || tree.FileCount != 1 || tree.ByteCount != 3 {
		t.Fatalf("unexpected golden tree: %#v", tree)
	}
	if _, err := validateArchiveEntries([]archiveEntry{{path: "K"}, {path: "K"}}); err == nil || !strings.Contains(err.Error(), "canonical caseless") {
		t.Fatalf("Unicode canonical caseless collision was not rejected: %v", err)
	}
	linkRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(linkRoot, "one"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(linkRoot, "one"), filepath.Join(linkRoot, "two")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTree(linkRoot); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("hard link was not rejected: %v", err)
	}
}

func TestAggregateTreeCeilingIsSharedByReadersAndStatus(t *testing.T) {
	var entries, bytes uint64
	if err := claimAggregateTree(&entries, &bytes, Tree{Entries: make([]TreeEntry, maxAggregateEntries), ByteCount: maxAggregateTreeByte}); err != nil {
		t.Fatal(err)
	}
	if err := claimAggregateTree(&entries, &bytes, Tree{Entries: []TreeEntry{{Path: "overflow"}}}); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate entry overflow was accepted: %v", err)
	}
}

func TestStatusRejectsAggregateTreeOverflow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := testAgentRoot(t)
	dependencies := make([]Dependency, 0, 3)
	for _, fixture := range []struct {
		name    string
		entries int
	}{{"alpha", maxTreeEntries}, {"beta", maxTreeEntries}, {"gamma", 1}} {
		root := filepath.Join(agent, "plugins", fixture.name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + fixture.name + `"}`)
		if err := os.WriteFile(filepath.Join(root, "plugin.json"), marker, 0o644); err != nil {
			t.Fatal(err)
		}
		for index := 1; index < fixture.entries; index++ {
			if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("entry-%04d", index)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		tree, err := ReadTree(root)
		if err != nil {
			t.Fatal(err)
		}
		markerSHA, err := markerDigest(tree, "plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		dependencies = append(dependencies, Dependency{
			Kind: Plugin, Name: fixture.name, Destination: "plugins/" + fixture.name,
			Source:       Source{Type: SourceLocal, Path: "../catalog/" + fixture.name},
			MarkerSHA256: markerSHA, TreeSHA256: tree.SHA256, FileCount: tree.FileCount, ByteCount: tree.ByteCount,
		})
	}
	lockBytes, err := EncodeLock(Lock{SchemaVersion: 1, Dependencies: dependencies})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, LockFilename), lockBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{AgentRoot: agent, Plugins: Hooks{Inspect: func(root string) (ComponentInfo, error) {
		return ComponentInfo{Name: filepath.Base(root), Marker: "plugin.json"}, nil
	}}}
	kind := Plugin
	if _, err := manager.Status(context.Background(), &kind, ""); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("status accepted aggregate tree overflow: %v", err)
	}
}

func TestLocalAndTLSArchiveMaterializeIdentically(t *testing.T) {
	agent := testAgentRoot(t)
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "review")
	writeFixtureTree(t, source)
	local, err := Materialize(context.Background(), agent, Selector{Type: "local", Path: source})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = local.Close() }()

	fixtures := map[string][]byte{"zip": zipFixture(t), "tar.gz": tarGzipFixture(t)}
	for format, payload := range fixtures {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write(payload)
			}))
			defer server.Close()
			previous := archiveTransport
			archiveTransport = server.Client().Transport
			defer func() { archiveTransport = previous }()
			digest := sha256.Sum256(payload)
			candidate, err := Materialize(context.Background(), agent, Selector{Type: "archive", URL: server.URL + "/component", SHA256: hex.EncodeToString(digest[:]), Subdirectory: "review"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = candidate.Close() }()
			if candidate.Tree.SHA256 != local.Tree.SHA256 || candidate.Tree.FileCount != local.Tree.FileCount || candidate.Tree.ByteCount != local.Tree.ByteCount {
				t.Fatalf("archive tree differs from local: local=%#v archive=%#v", local.Tree, candidate.Tree)
			}
			if candidate.Source.Format != format {
				t.Fatalf("archive format was not detected: %#v", candidate.Source)
			}
		})
	}
}

func TestPluginMaterializesIdenticallyFromLocalAndTLSArchives(t *testing.T) {
	agent := testAgentRoot(t)
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "review-pack")
	writePluginFixtureTree(t, source)
	local, err := Materialize(context.Background(), agent, Selector{Type: "local", Path: source})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = local.Close() }()
	tree, err := ReadTree(source)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string][]byte{
		"zip":    zipTreeFixture(t, "review-pack", tree),
		"tar.gz": tarGzipTreeFixture(t, "review-pack", tree),
	}
	for format, payload := range fixtures {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write(payload)
			}))
			defer server.Close()
			previous := archiveTransport
			archiveTransport = server.Client().Transport
			defer func() { archiveTransport = previous }()
			digest := sha256.Sum256(payload)
			candidate, err := Materialize(context.Background(), agent, Selector{Type: "archive", URL: server.URL + "/review-pack." + format, SHA256: hex.EncodeToString(digest[:]), Subdirectory: "review-pack"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = candidate.Close() }()
			if candidate.Tree.SHA256 != local.Tree.SHA256 || candidate.Tree.FileCount != local.Tree.FileCount || candidate.Tree.ByteCount != local.Tree.ByteCount {
				t.Fatalf("archive Plugin differs from local: local=%#v archive=%#v", local.Tree, candidate.Tree)
			}
			if candidate.Source.Format != format {
				t.Fatalf("archive format was not detected: %#v", candidate.Source)
			}
		})
	}
}

func TestArchiveRejectsRedirectAndDigestMismatch(t *testing.T) {
	agent := testAgentRoot(t)
	payload := zipFixture(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = writer.Write(payload) }))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	previous := archiveTransport
	archiveTransport = redirect.Client().Transport
	defer func() { archiveTransport = previous }()
	digest := sha256.Sum256(payload)
	if _, err := Materialize(context.Background(), agent, Selector{Type: "archive", URL: redirect.URL, SHA256: hex.EncodeToString(digest[:]), Format: "zip", Subdirectory: "review"}); err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatalf("redirect was not rejected: %v", err)
	}
	archiveTransport = target.Client().Transport
	if _, err := Materialize(context.Background(), agent, Selector{Type: "archive", URL: target.URL, SHA256: strings.Repeat("0", 64), Format: "zip", Subdirectory: "review"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("digest mismatch was not rejected: %v", err)
	}
	encoded := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		_, _ = writer.Write(payload)
	}))
	defer encoded.Close()
	archiveTransport = encoded.Client().Transport
	if _, err := Materialize(context.Background(), agent, Selector{Type: "archive", URL: encoded.URL, SHA256: hex.EncodeToString(digest[:]), Subdirectory: "review"}); err == nil || !strings.Contains(err.Error(), "response") {
		t.Fatalf("content-encoded archive response was accepted: %v", err)
	}
}

func TestArchiveRejectsUnsafeEntriesAndBounds(t *testing.T) {
	for name, entries := range map[string][]archiveEntry{
		"absolute":  {{path: "/escape"}},
		"escaping":  {{path: "../escape"}},
		"duplicate": {{path: "same"}, {path: "same"}},
		"file-parent": {
			{path: "parent"},
			{path: "parent/child"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateArchiveEntries(entries); err == nil {
				t.Fatal("unsafe archive entries were accepted")
			}
		})
	}

	var zipPayload bytes.Buffer
	zipWriter := zip.NewWriter(&zipPayload)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := zipWriter.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readZIP(zipPayload.Bytes()); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ZIP symlink was accepted: %v", err)
	}

	for name, header := range map[string]*tar.Header{
		"symlink":  {Name: "entry", Typeflag: tar.TypeSymlink, Linkname: "target"},
		"hardlink": {Name: "entry", Typeflag: tar.TypeLink, Linkname: "target"},
		"device":   {Name: "entry", Typeflag: tar.TypeChar},
		"pipe":     {Name: "entry", Typeflag: tar.TypeFifo},
		"special":  {Name: "entry", Typeflag: tar.TypeReg, Mode: 0o4755},
	} {
		t.Run("tar-"+name, func(t *testing.T) {
			var payload bytes.Buffer
			writer := tar.NewWriter(&payload)
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readTAR(tar.NewReader(bytes.NewReader(payload.Bytes()))); err == nil {
				t.Fatal("unsafe TAR entry was accepted")
			}
		})
	}

	var oversized bytes.Buffer
	writer := tar.NewWriter(&oversized)
	if err := writer.WriteHeader(&tar.Header{Name: "large", Typeflag: tar.TypeReg, Size: maxFileBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := readTAR(tar.NewReader(bytes.NewReader(oversized.Bytes()))); err == nil || !strings.Contains(err.Error(), "bounds") {
		t.Fatalf("oversized expansion was accepted: %v", err)
	}
}

func TestRemoteRootSkillUsesSourceBasename(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "review")
	writeFixtureTree(t, source)
	tree, err := ReadTree(source)
	if err != nil {
		t.Fatal(err)
	}
	payload := zipTreeFixture(t, "", tree)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	previous := archiveTransport
	archiveTransport = server.Client().Transport
	defer func() { archiveTransport = previous }()
	digest := sha256.Sum256(payload)
	selector := func(agent, name string) (Dependency, error) {
		manager := Manager{AgentRoot: agent, Skills: Hooks{Inspect: func(string) (ComponentInfo, error) {
			return ComponentInfo{Name: "review", Marker: "SKILL.md"}, nil
		}}}
		return manager.Add(context.Background(), Skill, Selector{Type: "archive", URL: server.URL + "/" + name + ".zip", SHA256: hex.EncodeToString(digest[:])})
	}
	if dependency, err := selector(testAgentRoot(t), "review"); err != nil || dependency.Name != "review" {
		t.Fatalf("matching remote root Skill was rejected: %#v %v", dependency, err)
	}
	if _, err := selector(testAgentRoot(t), "not-review"); err == nil || !strings.Contains(err.Error(), "basename") {
		t.Fatalf("mismatched remote root Skill was accepted: %v", err)
	}
	manager := Manager{AgentRoot: testAgentRoot(t), Skills: Hooks{Inspect: func(string) (ComponentInfo, error) {
		return ComponentInfo{Name: "review", Marker: "SKILL.md"}, nil
	}}}
	if _, err := manager.Add(context.Background(), Skill, Selector{Type: "archive", URL: server.URL + "/", SHA256: hex.EncodeToString(digest[:])}); err == nil || !strings.Contains(err.Error(), "basename") {
		t.Fatalf("remote root Skill without a source basename was accepted: %v", err)
	}
}

func TestInterruptedPublicationRecoversBeforeNextRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := testAgentRoot(t)
	source := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := []byte("---\nname: review\ndescription: Review.\n---\n")
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := Manager{AgentRoot: agent, Skills: Hooks{Inspect: func(root string) (ComponentInfo, error) {
		return ComponentInfo{Name: "review", Marker: "SKILL.md"}, nil
	}}}
	publicationInterruptionHook = func() error { return context.Canceled }
	_, err := manager.Add(context.Background(), Skill, Selector{Type: "local", Path: source})
	publicationInterruptionHook = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publication interruption was not surfaced: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(agent, journalFilename)); err != nil {
		t.Fatalf("recovery journal was not retained: %v", err)
	}
	snapshot, err := ReadSnapshot(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Dependencies) != 1 || snapshot.Dependencies[0].Dependency.Name != "review" {
		t.Fatalf("interrupted add was not recovered: %#v", snapshot)
	}
	if _, err := os.Lstat(filepath.Join(agent, journalFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery journal remained after recovery: %v", err)
	}
}

func TestLoopbackTLSGitMaterializesWithoutCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	agent := testAgentRoot(t)
	webRoot := t.TempDir()
	repository := filepath.Join(webRoot, "component.git")
	runTestGit(t, "", "init", "--bare", repository)
	work := filepath.Join(t.TempDir(), "work")
	runTestGit(t, "", "init", "-b", "main", work)
	runTestGit(t, work, "config", "user.name", "Fixture")
	runTestGit(t, work, "config", "user.email", "fixture@example.com")
	writeFixtureTree(t, filepath.Join(work, "review"))
	writePluginFixtureTree(t, filepath.Join(work, "review-pack"))
	if err := os.WriteFile(filepath.Join(work, "review", ".gitattributes"), []byte("ignored.txt export-ignore\nsubst.txt export-subst\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "review", "ignored.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "review", "subst.txt"), []byte("$Format:%H$\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "review", "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "review-pack", "empty")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, work, "add", "review", "review-pack")
	runTestGit(t, work, "commit", "-m", "fixture")
	runTestGit(t, work, "remote", "add", "origin", repository)
	runTestGit(t, work, "push", "origin", "main")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		command := exec.Command("git", "http-backend")
		command.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+webRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+request.URL.Path,
			"REQUEST_METHOD="+request.Method,
			"QUERY_STRING="+request.URL.RawQuery,
			"CONTENT_TYPE="+request.Header.Get("Content-Type"),
			fmt.Sprintf("CONTENT_LENGTH=%d", request.ContentLength),
		)
		command.Stdin = request.Body
		output, err := command.Output()
		if err != nil {
			http.Error(writer, "backend failed", http.StatusInternalServerError)
			return
		}
		head, body, ok := bytes.Cut(output, []byte("\r\n\r\n"))
		if !ok {
			http.Error(writer, "backend response invalid", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for _, line := range bytes.Split(head, []byte("\r\n")) {
			key, value, found := bytes.Cut(line, []byte(":"))
			if !found {
				continue
			}
			if strings.EqualFold(string(key), "Status") {
				_, _ = fmt.Sscanf(strings.TrimSpace(string(value)), "%d", &status)
				continue
			}
			writer.Header().Add(string(key), strings.TrimSpace(string(value)))
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(certificate, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", certificate)
	candidate, err := Materialize(context.Background(), agent, Selector{Type: "git", URL: server.URL + "/component.git", Ref: "refs/heads/main", Subdirectory: "review"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = candidate.Close() }()
	if !commitPattern.MatchString(candidate.Source.Commit) || candidate.Source.Ref != "refs/heads/main" {
		t.Fatalf("Git source was not pinned: %#v", candidate.Source)
	}
	if _, err := os.Lstat(filepath.Join(candidate.Root, ".git-source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git object database leaked into candidate: %v", err)
	}
	local, err := ReadTree(filepath.Join(work, "review"))
	if err != nil || local.SHA256 != candidate.Tree.SHA256 {
		t.Fatalf("Git and local materialization differ: %#v %#v %v", local, candidate.Tree, err)
	}
	pluginCandidate, err := Materialize(context.Background(), agent, Selector{Type: "git", URL: server.URL + "/component.git", Ref: "refs/heads/main", Subdirectory: "review-pack"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pluginCandidate.Close() }()
	localPlugin, err := ReadTree(filepath.Join(work, "review-pack"))
	if err != nil || localPlugin.SHA256 != pluginCandidate.Tree.SHA256 {
		t.Fatalf("Git and local Plugin materialization differ: %#v %#v %v", localPlugin, pluginCandidate.Tree, err)
	}
	firstCommit, firstTree := candidate.Source.Commit, candidate.Tree.SHA256
	if err := os.WriteFile(filepath.Join(work, "review", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, work, "add", "review/new.txt")
	runTestGit(t, work, "commit", "-m", "move branch")
	runTestGit(t, work, "push", "origin", "main")
	moved, err := Materialize(context.Background(), agent, Selector{Type: "git", URL: server.URL + "/component.git", Ref: "refs/heads/main", Subdirectory: "review"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = moved.Close() }()
	if moved.Source.Commit == firstCommit || moved.Tree.SHA256 == firstTree || !commitPattern.MatchString(moved.Source.Commit) {
		t.Fatalf("moving ref did not resolve to a new exact commit and tree: %#v", moved.Source)
	}
}

func TestGitCredentialHelperOutputIsSuppressed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	agent := testAgentRoot(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
		http.Error(writer, "remote-secret", http.StatusUnauthorized)
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(certificate, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	helperDirectory := t.TempDir()
	helper := filepath.Join(helperDirectory, "credential-helper")
	sentinel := filepath.Join(helperDirectory, "called")
	script := "#!/bin/sh\nprintf seen > \"$HCTL_HELPER_SENTINEL\"\nprintf 'username=fixture\\npassword=helper-secret\\n'\nprintf 'helper-stderr-secret\\n' >&2\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(helperDirectory, "gitconfig")
	if err := os.WriteFile(config, []byte("[credential]\n\thelper = "+helper+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", certificate)
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("HCTL_HELPER_SENTINEL", sentinel)
	_, err := Materialize(context.Background(), agent, Selector{Type: "git", URL: server.URL + "/component.git", Ref: "refs/heads/main"})
	if err == nil {
		t.Fatal("unauthorized Git source succeeded")
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("credential helper was not exercised: %v", statErr)
	}
	for _, secret := range []string{"helper-secret", "helper-stderr-secret", "remote-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Git diagnostic retained helper or remote output: %v", err)
		}
	}
}

func TestReaderSerializesWithMutationUntilSnapshotPublication(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := testAgentRoot(t)
	source := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: review\ndescription: Review.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approvalStarted := make(chan struct{})
	releaseApproval := make(chan struct{})
	manager := Manager{
		AgentRoot: agent,
		Skills: Hooks{Inspect: func(string) (ComponentInfo, error) {
			return ComponentInfo{Name: "review", Marker: "SKILL.md"}, nil
		}},
		Approve: func(TrustSummary) error {
			close(approvalStarted)
			<-releaseApproval
			return nil
		},
	}
	addResult := make(chan error, 1)
	go func() {
		_, err := manager.Add(context.Background(), Skill, Selector{Type: "local", Path: source})
		addResult <- err
	}()
	<-approvalStarted
	readResult := make(chan error, 1)
	go func() {
		_, err := ReadSnapshot(context.Background(), agent)
		readResult <- err
	}()
	select {
	case err := <-readResult:
		t.Fatalf("reader escaped active mutation lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseApproval)
	if err := <-addResult; err != nil {
		t.Fatal(err)
	}
	if err := <-readResult; err != nil {
		t.Fatal(err)
	}
}

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func testAgentRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "instructions.md"), []byte("---\ndescription: Test.\n---\n\nTest.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFixtureTree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: review\ndescription: Review.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 2, 255}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writePluginFixtureTree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 2, 255}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mcp := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"review-server":{"type":"stdio","command":"./server.sh"}}}`
	if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: Review.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func zipTreeFixture(t *testing.T, rootName string, tree Tree) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, treeEntry := range tree.Entries {
		name := treeEntry.Path
		if rootName != "" {
			name = rootName + "/" + name
		}
		mode := os.FileMode(0o644)
		if treeEntry.Directory {
			name += "/"
			mode = os.ModeDir | 0o755
		} else if treeEntry.Executable {
			mode = 0o755
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(treeEntry.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func tarGzipTreeFixture(t *testing.T, rootName string, tree Tree) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	for _, treeEntry := range tree.Entries {
		typeflag := byte(tar.TypeReg)
		mode := int64(0o644)
		if treeEntry.Directory {
			typeflag = tar.TypeDir
			mode = 0o755
		} else if treeEntry.Executable {
			mode = 0o755
		}
		name := treeEntry.Path
		if rootName != "" {
			name = rootName + "/" + name
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Typeflag: typeflag, Size: int64(len(treeEntry.Content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(treeEntry.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func zipFixture(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	files := []struct {
		name string
		mode os.FileMode
		data []byte
	}{{"review/empty/", os.ModeDir | 0o755, nil}, {"review/SKILL.md", 0o644, []byte("---\nname: review\ndescription: Review.\n---\n")}, {"review/binary", 0o644, []byte{0, 1, 2, 255}}, {"review/run.sh", 0o755, []byte("#!/bin/sh\n")}}
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(file.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func tarGzipFixture(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	entries := []struct {
		name     string
		mode     int64
		typeflag byte
		data     []byte
	}{{"review/empty/", 0o755, tar.TypeDir, nil}, {"review/SKILL.md", 0o644, tar.TypeReg, []byte("---\nname: review\ndescription: Review.\n---\n")}, {"review/binary", 0o644, tar.TypeReg, []byte{0, 1, 2, 255}}, {"review/run.sh", 0o755, tar.TypeReg, []byte("#!/bin/sh\n")}}
	for _, entry := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: entry.typeflag, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
