package discord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"hctl/internal/gateway"
	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/session"
)

const (
	testApplication = "123456789012345678"
	testUser        = "234567890123456789"
)

func TestSignedInteractionsDriveOneFIFOConversation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan deliveredRequest, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- deliveredRequest{Method: request.Method, Path: request.URL.Path, Body: body}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	p := discordProject(t, "claude")
	submissions := make(chan gateway.Submission, 32)
	var audit bytes.Buffer
	adapter, err := New(Config{
		ApplicationID: testApplication,
		AllowedUserID: testUser,
		PublicKey:     publicKey,
		Listen:        "127.0.0.1:0",
		APIBase:       upstream.URL,
		Audit:         &audit,
		Now:           func() time.Time { return time.Unix(2_000_000_000, 0) },
	}, submissions)
	if err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{started: make(chan string, 4), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- gateway.RunSubmissions(context.Background(), p, driver, "discord", submissions, func(event gateway.Event) error { adapter.HandleEvent(event); return nil })
	}()

	first := signedCommand(t, adapter.Handler(), privateKey, "345678901234567890", "token-first", "first")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"type":5`) {
		t.Fatalf("deferred response = %d %s", first.Code, first.Body.String())
	}
	if got := <-driver.started; got != "345678901234567890" {
		t.Fatalf("first turn = %q", got)
	}
	// The HTTP acknowledgement completed while the harness turn is blocked.
	second := signedCommand(t, adapter.Handler(), privateKey, "456789012345678901", "token-second", "second")
	duplicate := signedCommand(t, adapter.Handler(), privateKey, "456789012345678901", "token-second", "second")
	if second.Code != http.StatusOK || duplicate.Code != http.StatusOK {
		t.Fatalf("queued responses = %d, %d", second.Code, duplicate.Code)
	}
	close(driver.release)
	if got := <-driver.started; got != "456789012345678901" {
		t.Fatalf("second turn = %q", got)
	}
	close(submissions)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	adapter.Close()

	if !reflect.DeepEqual(driver.inputs, []string{"345678901234567890", "456789012345678901"}) {
		t.Fatalf("turn order/deduplication = %v", driver.inputs)
	}
	var delivered []deliveredRequest
	for len(requests) > 0 {
		delivered = append(delivered, <-requests)
	}
	if len(delivered) != 4 {
		t.Fatalf("delivery count = %d, want 4: %#v", len(delivered), delivered)
	}
	for index, request := range delivered {
		if index == 0 || index == 2 {
			if request.Method != http.MethodPatch || !strings.HasSuffix(request.Path, "/messages/@original") {
				t.Fatalf("initial response request = %#v", request)
			}
		} else if request.Method != http.MethodPost {
			t.Fatalf("followup request = %#v", request)
		}
		var payload struct {
			Content         string `json:"content"`
			AllowedMentions struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
		}
		if json.Unmarshal(request.Body, &payload) != nil || len([]rune(payload.Content)) > 2_000 || payload.AllowedMentions.Parse == nil || len(payload.AllowedMentions.Parse) != 0 {
			t.Fatalf("unsafe delivery payload = %s", request.Body)
		}
	}
	stateBytes, err := os.ReadFile(filepath.Join(p.WorkspaceRoot, ".hctl", "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"token-first", "token-second", "X-Signature", "first", "second", strings.Repeat("x", 2_500)} {
		if bytes.Contains(stateBytes, []byte(prohibited)) || strings.Contains(audit.String(), prohibited) {
			t.Fatalf("channel content escaped managed memory: %q", prohibited)
		}
	}
	if strings.Count(audit.String(), "delivery=completed") != 2 {
		t.Fatalf("safe delivery audit = %q", audit.String())
	}
}

func TestInteractionAuthenticationAndValidation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	submissions := make(chan gateway.Submission)
	adapter, err := New(Config{ApplicationID: testApplication, AllowedUserID: testUser, PublicKey: publicKey, Listen: "[::1]:0", Now: func() time.Time { return now }}, submissions)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	ping := signedInteraction(t, adapter.Handler(), privateKey, now, map[string]any{"id": "345678901234567890", "application_id": testApplication, "type": 1}, nil)
	if ping.Code != http.StatusOK || !strings.Contains(ping.Body.String(), `"type":1`) {
		t.Fatalf("PING = %d %s", ping.Code, ping.Body.String())
	}

	valid := commandBody("345678901234567890", testApplication, testUser, "token", "hello")
	tests := map[string]struct {
		body       map[string]any
		key        ed25519.PrivateKey
		at         time.Time
		mutate     func(*http.Request)
		wantStatus int
	}{
		"missing signature": {valid, privateKey, now, func(request *http.Request) { request.Header.Del("X-Signature-Ed25519") }, http.StatusUnauthorized},
		"bad signature": {valid, privateKey, now, func(request *http.Request) {
			request.Header.Set("X-Signature-Ed25519", strings.Repeat("00", ed25519.SignatureSize))
		}, http.StatusUnauthorized},
		"stale signature":   {valid, privateKey, now.Add(-maxClockSkew - time.Second), nil, http.StatusUnauthorized},
		"wrong application": {commandBody("345678901234567890", "999", testUser, "token", "hello"), privateKey, now, nil, http.StatusForbidden},
		"wrong user":        {commandBody("345678901234567890", testApplication, "999", "token", "hello"), privateKey, now, nil, http.StatusForbidden},
		"empty message":     {commandBody("345678901234567890", testApplication, testUser, "token", " "), privateKey, now, nil, http.StatusBadRequest},
		"oversized message": {commandBody("345678901234567890", testApplication, testUser, "token", strings.Repeat("x", maxMessageBytes+1)), privateKey, now, nil, http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			response := signedInteraction(t, adapter.Handler(), test.key, test.at, test.body, test.mutate)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestOutboundDeliveryFailureClassesAndBounds(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	job := delivery{inputID: "345678901234567890", token: &tokenEntry{token: "opaque-token", ready: ready}, status: "completed", content: "done"}
	tests := map[string]struct {
		handler http.HandlerFunc
		timeout time.Duration
		want    string
	}{
		"rate limited": {func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTooManyRequests) }, time.Second, "rate_limited"},
		"redirect": {func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Location", "/elsewhere")
			response.WriteHeader(http.StatusFound)
		}, time.Second, "failed"},
		"oversized response": {func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
		}, time.Second, "uncertain"},
		"timeout": {func(response http.ResponseWriter, _ *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = response.Write([]byte(`{}`))
		}, time.Millisecond, "uncertain"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			adapter := outboundAdapter(t, server.URL, test.timeout)
			if got := adapter.send(job); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
			adapter.Close()
		})
	}
}

func TestFailureAndUncertainMessagesAreBounded(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{{"failed", "Agent turn failed."}, {"uncertain", "uncertain"}, {"completed", "Agent turn completed."}} {
		chunks := responseChunks("", test.status, false)
		if len(chunks) != 1 || !strings.Contains(chunks[0], test.want) {
			t.Fatalf("%s chunks = %v", test.status, chunks)
		}
	}
	chunks := responseChunks(strings.Repeat("界", maxOutputRunes), "completed", true)
	if len(chunks) != maxChunks || !strings.Contains(chunks[len(chunks)-1], "[output truncated]") {
		t.Fatalf("bounded chunks = %d, last %q", len(chunks), chunks[len(chunks)-1])
	}
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > maxChunkRunes {
			t.Fatalf("chunk exceeded Discord limit: %d", utf8.RuneCountInString(chunk))
		}
	}
}

func TestRecoveredInteractionDeliversUncertainWithoutRetry(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		delivered <- string(body)
		_, _ = response.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	p := discordProject(t, "claude")
	interactionID := "345678901234567890"
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "discord", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept(interactionID, "do not retry me"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	submissions := make(chan gateway.Submission, 1)
	adapter, err := New(Config{
		ApplicationID: testApplication, AllowedUserID: testUser, PublicKey: publicKey,
		Listen: "127.0.0.1:0", APIBase: upstream.URL,
		Now: func() time.Time { return time.Unix(2_000_000_000, 0) },
	}, submissions)
	if err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{started: make(chan string, 1), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- gateway.RunSubmissions(context.Background(), p, driver, "discord", submissions, func(event gateway.Event) error { adapter.HandleEvent(event); return nil })
	}()
	response := signedCommand(t, adapter.Handler(), privateKey, interactionID, "replacement-token", "do not retry me")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":5`) {
		t.Fatalf("uncertain acknowledgement = %d %s", response.Code, response.Body.String())
	}
	close(submissions)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	adapter.Close()
	select {
	case body := <-delivered:
		if !strings.Contains(body, "uncertain") {
			t.Fatalf("uncertain delivery = %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("uncertain outcome was not delivered")
	}
	select {
	case id := <-driver.started:
		t.Fatalf("uncertain input was retried as turn %s", id)
	default:
	}
}

func TestFailedTurnIsDelivered(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		delivered <- string(body)
		_, _ = response.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	p := discordProject(t, "claude")
	submissions := make(chan gateway.Submission, 1)
	adapter, err := New(Config{
		ApplicationID: testApplication, AllowedUserID: testUser, PublicKey: publicKey,
		Listen: "127.0.0.1:0", APIBase: upstream.URL,
		Now: func() time.Time { return time.Unix(2_000_000_000, 0) },
	}, submissions)
	if err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{started: make(chan string, 1), status: "failed", output: ""}
	done := make(chan error, 1)
	go func() {
		done <- gateway.RunSubmissions(context.Background(), p, driver, "discord", submissions, func(event gateway.Event) error { adapter.HandleEvent(event); return nil })
	}()
	response := signedCommand(t, adapter.Handler(), privateKey, "345678901234567890", "failure-token", "fail")
	if response.Code != http.StatusOK {
		t.Fatalf("failed turn acknowledgement = %d", response.Code)
	}
	close(submissions)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	adapter.Close()
	if body := <-delivered; !strings.Contains(body, "Agent turn failed") {
		t.Fatalf("failed turn delivery = %s", body)
	}
}

func TestRuntimeConfigurationRejectsExposedOrAmbiguousIdentity(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := Config{ApplicationID: testApplication, AllowedUserID: testUser, PublicKey: publicKey, Listen: "127.0.0.1:0", Path: DefaultPath}
	tests := map[string]Config{
		"public listener":   withConfig(base, func(config *Config) { config.Listen = "0.0.0.0:8787" }),
		"hostname listener": withConfig(base, func(config *Config) { config.Listen = "localhost:8787" }),
		"unclean path":      withConfig(base, func(config *Config) { config.Path = "/a/../interactions" }),
		"missing app":       withConfig(base, func(config *Config) { config.ApplicationID = "" }),
		"missing user":      withConfig(base, func(config *Config) { config.AllowedUserID = "" }),
		"bad key":           withConfig(base, func(config *Config) { config.PublicKey = []byte("short") }),
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRuntime(config); err == nil {
				t.Fatal("unsafe runtime configuration was accepted")
			}
		})
	}
	if _, err := ParsePublicKey(hex.EncodeToString(publicKey)); err != nil {
		t.Fatal(err)
	}
}

type deliveredRequest struct {
	Method string
	Path   string
	Body   []byte
}

type fakeDriver struct {
	started chan string
	release chan struct{}
	inputs  []string
	status  string
	output  string
	mu      sync.Mutex
}

func (d *fakeDriver) Name() string                 { return "claude" }
func (d *fakeDriver) Executable() string           { return "/fake/claude" }
func (d *fakeDriver) Verify(context.Context) error { return nil }
func (d *fakeDriver) Open(context.Context, string, string) (harness.Session, error) {
	return &fakeSession{driver: d}, nil
}

type fakeSession struct{ driver *fakeDriver }

func (s *fakeSession) InitialEvents() []harness.Event { return nil }
func (s *fakeSession) RunTurn(_ context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	s.driver.mu.Lock()
	s.driver.inputs = append(s.driver.inputs, input.ID)
	index := len(s.driver.inputs)
	s.driver.mu.Unlock()
	s.driver.started <- input.ID
	if index == 1 && s.driver.release != nil {
		<-s.driver.release
	}
	output := s.driver.output
	if output == "" && s.driver.status == "" {
		output = strings.Repeat("x", 2_500)
	}
	if output != "" {
		emit(harness.Event{Type: "agent.output.delta", TurnID: input.ID, Delta: output})
	}
	status := s.driver.status
	if status == "" {
		status = "completed"
	}
	return harness.TurnResult{TurnID: input.ID, Status: status}, nil
}
func (s *fakeSession) Close() error { return nil }
func (s *fakeSession) Abort()       {}

func signedCommand(t *testing.T, handler http.Handler, key ed25519.PrivateKey, id, token, message string) *httptest.ResponseRecorder {
	t.Helper()
	return signedInteraction(t, handler, key, time.Unix(2_000_000_000, 0), commandBody(id, testApplication, testUser, token, message), nil)
}

func commandBody(id, application, user, token, message string) map[string]any {
	return map[string]any{
		"id": id, "application_id": application, "type": 2, "token": token,
		"member": map[string]any{"user": map[string]any{"id": user}},
		"data":   map[string]any{"options": []any{map[string]any{"name": "message", "type": 3, "value": message}}},
	}
}

func signedInteraction(t *testing.T, handler http.Handler, key ed25519.PrivateKey, at time.Time, bodyValue map[string]any, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(bodyValue)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := strconv.FormatInt(at.Unix(), 10)
	signature := ed25519.Sign(key, append([]byte(timestamp), body...))
	request := httptest.NewRequest(http.MethodPost, DefaultPath, bytes.NewReader(body))
	request.Header.Set("X-Signature-Timestamp", timestamp)
	request.Header.Set("X-Signature-Ed25519", hex.EncodeToString(signature))
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func outboundAdapter(t *testing.T, apiBase string, timeout time.Duration) *Adapter {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	submissions := make(chan gateway.Submission)
	adapter, err := New(Config{
		ApplicationID: testApplication,
		AllowedUserID: testUser,
		PublicKey:     publicKey,
		Listen:        "127.0.0.1:0",
		APIBase:       apiBase,
		HTTPClient:    &http.Client{Timeout: timeout},
	}, submissions)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func withConfig(config Config, change func(*Config)) Config {
	change(&config)
	return config
}

func discordProject(t *testing.T, harnessName string) *project.Project {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n")
	writeTestFile(t, filepath.Join(root, "channels", "discord.md"), "Receive signed test commands.\n")
	p, err := project.Load(root, harnessName)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeTestFile(t *testing.T, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}
