package discord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hctl/internal/gateway"
	"hctl/internal/harness"
	"hctl/internal/project"
)

const (
	DefaultListen = "127.0.0.1:8787"
	DefaultPath   = "/interactions"
	DiscordAPI    = "https://discord.com/api/v10"

	maxRequestBytes  = 64 << 10
	maxResponseBytes = 64 << 10
	maxMessageBytes  = 32 << 10
	maxChunks        = 6
	maxChunkRunes    = 2_000
	maxOutputRunes   = maxChunks*maxChunkRunes - 64
	maxTokenBytes    = 512
	maxPendingTurns  = 32
	maxClockSkew     = 5 * time.Minute
	tokenLifetime    = 15 * time.Minute
	defaultExpiry    = 14 * time.Minute
)

type Config struct {
	ApplicationID string
	AllowedUserID string
	PublicKey     ed25519.PublicKey
	Listen        string
	Path          string
	APIBase       string
	HTTPClient    *http.Client
	Now           func() time.Time
	ExpiryAfter   time.Duration
	Audit         io.Writer
	Diagnostics   io.Writer
}

type interaction struct {
	ID            string          `json:"id"`
	ApplicationID string          `json:"application_id"`
	Type          int             `json:"type"`
	Token         string          `json:"token"`
	Data          interactionData `json:"data"`
	Member        struct {
		User interactionUser `json:"user"`
	} `json:"member"`
	User interactionUser `json:"user"`
}

type interactionUser struct {
	ID string `json:"id"`
}

type interactionData struct {
	Options []interactionOption `json:"options"`
}

type interactionOption struct {
	Name  string          `json:"name"`
	Type  int             `json:"type"`
	Value json.RawMessage `json:"value"`
}

type tokenEntry struct {
	token     string
	expiresAt time.Time
	done      chan struct{}
	once      sync.Once
}

func (entry *tokenEntry) release() { entry.once.Do(func() { close(entry.done) }) }

type turn struct {
	token     *tokenEntry
	output    strings.Builder
	runes     int
	truncated bool
}

type delivery struct {
	inputID   string
	token     *tokenEntry
	content   string
	status    string
	truncated bool
}

type Adapter struct {
	config      Config
	submissions chan<- gateway.Submission
	client      *http.Client
	apiBase     string

	mu      sync.Mutex
	turns   map[string]*turn
	stopped chan struct{}
	stop    sync.Once
	workers sync.WaitGroup
	auditMu sync.Mutex
}

func New(config Config, submissions chan<- gateway.Submission) (*Adapter, error) {
	if submissions == nil {
		return nil, errors.New("discord gateway input is required")
	}
	if err := ValidateRuntime(config); err != nil {
		return nil, err
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ExpiryAfter == 0 {
		config.ExpiryAfter = defaultExpiry
	}
	if config.ExpiryAfter < 0 || config.ExpiryAfter > defaultExpiry {
		return nil, errors.New("discord response expiry must be positive and at most 14 minutes")
	}
	if config.Audit == nil {
		config.Audit = io.Discard
	}
	if config.Diagnostics == nil {
		config.Diagnostics = io.Discard
	}
	if config.APIBase == "" {
		config.APIBase = DiscordAPI
	}
	base, err := validateAPIBase(config.APIBase)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if config.HTTPClient != nil {
		copy := *config.HTTPClient
		client = &copy
		if client.Timeout <= 0 {
			client.Timeout = 5 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a := &Adapter{
		config:      config,
		submissions: submissions,
		client:      client,
		apiBase:     strings.TrimSuffix(base.String(), "/"),
		turns:       map[string]*turn{},
		stopped:     make(chan struct{}),
	}
	return a, nil
}

func ValidateRuntime(config Config) error {
	if !validSnowflake(config.ApplicationID) {
		return errors.New("discord application ID must be a nonzero decimal snowflake")
	}
	if !validSnowflake(config.AllowedUserID) {
		return errors.New("discord allowed user must be a nonzero decimal snowflake")
	}
	if len(config.PublicKey) != ed25519.PublicKeySize {
		return errors.New("discord public key must be 32 bytes")
	}
	listen := config.Listen
	if listen == "" {
		listen = DefaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return errors.New("discord listen address must include a loopback IP and port")
	}
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("discord listener must use a loopback IP and valid port")
	}
	requestPath := config.Path
	if requestPath == "" {
		requestPath = DefaultPath
	}
	if len(requestPath) > 128 || !strings.HasPrefix(requestPath, "/") || path.Clean(requestPath) != requestPath || strings.ContainsAny(requestPath, "?#") {
		return errors.New("discord interaction path must be a clean absolute path")
	}
	return nil
}

func ParsePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("discord public key must be 64 hexadecimal characters")
	}
	return ed25519.PublicKey(decoded), nil
}

func DefaultConversation(applicationID, userID string) string {
	return "discord-" + applicationID + "-" + userID
}

func (a *Adapter) Handler() http.Handler { return http.HandlerFunc(a.handle) }

func (a *Adapter) HandleEvent(event gateway.Event) {
	if event.InputID == "" {
		return
	}
	a.mu.Lock()
	current := a.turns[event.InputID]
	if current == nil {
		a.mu.Unlock()
		return
	}
	if event.Type == "agent.output.delta" {
		appendBounded(current, event.Delta)
		a.mu.Unlock()
		return
	}
	status := terminalStatus(event)
	if status == "" {
		a.mu.Unlock()
		return
	}
	job := delivery{inputID: event.InputID, token: current.token, content: current.output.String(), status: status, truncated: current.truncated}
	current.token.release()
	delete(a.turns, event.InputID)
	a.mu.Unlock()
	a.dispatch(job)
}

func (a *Adapter) Close() {
	a.Stop()
	a.workers.Wait()
}

func (a *Adapter) Stop() {
	a.stop.Do(func() {
		close(a.stopped)
		a.mu.Lock()
		for _, current := range a.turns {
			if current.token != nil {
				current.token.release()
			}
		}
		a.turns = map[string]*turn{}
		a.mu.Unlock()
	})
}

func (a *Adapter) handle(response http.ResponseWriter, request *http.Request) {
	requestPath := a.config.Path
	if requestPath == "" {
		requestPath = DefaultPath
	}
	if request.URL.Path != requestPath {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxRequestBytes))
	if err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	signedAt, verified := a.verify(request.Header, body)
	if !verified {
		http.Error(response, "invalid signature", http.StatusUnauthorized)
		return
	}
	var incoming interaction
	if err := json.Unmarshal(body, &incoming); err != nil || !validSnowflake(incoming.ID) {
		http.Error(response, "invalid interaction", http.StatusBadRequest)
		return
	}
	if incoming.ApplicationID != a.config.ApplicationID {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	if incoming.Type == 1 {
		_ = writeJSON(response, http.StatusOK, map[string]any{"type": 1})
		return
	}
	if incoming.Type != 2 {
		http.Error(response, "unsupported interaction", http.StatusBadRequest)
		return
	}
	userID := incoming.Member.User.ID
	if userID == "" {
		userID = incoming.User.ID
	}
	if userID != a.config.AllowedUserID {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}
	message, ok := commandMessage(incoming)
	if !ok || !validToken(incoming.Token) {
		http.Error(response, "invalid command", http.StatusBadRequest)
		return
	}
	entry := &tokenEntry{token: incoming.Token, expiresAt: signedAt.Add(tokenLifetime), done: make(chan struct{})}
	a.mu.Lock()
	current := a.turns[incoming.ID]
	overloaded := current == nil && len(a.turns) >= maxPendingTurns
	if overloaded {
		a.mu.Unlock()
	} else {
		if current == nil {
			current = &turn{}
			a.turns[incoming.ID] = current
		}
		if current.token != nil {
			current.token.release()
		}
		current.token = entry
		a.mu.Unlock()
	}
	if overloaded {
		entry.release()
		_ = writeJSON(response, http.StatusOK, immediateMessage(rejectionText("queue_full")))
		return
	}

	if err := writeJSON(response, http.StatusOK, map[string]any{"type": 5, "data": map[string]any{"allowed_mentions": allowedMentions()}}); err != nil {
		a.discard(incoming.ID, entry)
		return
	}
	if err := http.NewResponseController(response).Flush(); err != nil {
		a.discard(incoming.ID, entry)
		return
	}
	a.workers.Add(2)
	go func() {
		defer a.workers.Done()
		a.expireAfter(incoming.ID, entry, signedAt.Add(a.config.ExpiryAfter).Sub(a.config.Now()))
	}()
	go func() {
		defer a.workers.Done()
		a.submit(incoming.ID, message, entry)
	}()
}

func (a *Adapter) verify(header http.Header, body []byte) (time.Time, bool) {
	timestamp := header.Get("X-Signature-Timestamp")
	signature, err := hex.DecodeString(header.Get("X-Signature-Ed25519"))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	signedAt := time.Unix(seconds, 0)
	delta := a.config.Now().Sub(signedAt)
	if delta < -maxClockSkew || delta > maxClockSkew {
		return time.Time{}, false
	}
	message := make([]byte, 0, len(timestamp)+len(body))
	message = append(message, timestamp...)
	message = append(message, body...)
	return signedAt, ed25519.Verify(a.config.PublicKey, message, signature)
}

func (a *Adapter) submit(inputID, message string, entry *tokenEntry) {
	reply := make(chan gateway.SubmissionResult, 1)
	select {
	case a.submissions <- gateway.Submission{InputID: inputID, Text: message, Reply: reply}:
	case <-entry.done:
		return
	case <-a.stopped:
		return
	}
	var result gateway.SubmissionResult
	select {
	case result = <-reply:
	case <-entry.done:
		return
	case <-a.stopped:
		return
	}
	if result.Status != "queued" && result.Status != "active" && !result.Duplicate {
		a.finish(inputID, entry, delivery{content: rejectionText(result.Status), status: "rejected"})
		return
	}
	if result.Duplicate && terminalOutcome(result.Status) {
		a.finish(inputID, entry, delivery{status: result.Status})
	}
}

func (a *Adapter) expireAfter(inputID string, entry *tokenEntry, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		if job, ok := a.detach(inputID, entry, delivery{content: "Discord response window expired; the agent may still be running.", status: "expired"}); ok {
			a.record(job, a.send(job))
		}
	case <-entry.done:
	case <-a.stopped:
	}
}

func (a *Adapter) finish(inputID string, entry *tokenEntry, job delivery) {
	if job, ok := a.detach(inputID, entry, job); ok {
		a.dispatch(job)
	}
}

func (a *Adapter) detach(inputID string, entry *tokenEntry, job delivery) (delivery, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if current := a.turns[inputID]; current != nil && current.token == entry {
		job.inputID = inputID
		job.token = entry
		if job.content == "" {
			job.content = current.output.String()
			job.truncated = current.truncated
		}
		delete(a.turns, inputID)
		entry.release()
		return job, true
	}
	return delivery{}, false
}

func (a *Adapter) discard(inputID string, entry *tokenEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if current := a.turns[inputID]; current != nil && current.token == entry {
		delete(a.turns, inputID)
		entry.release()
	}
}

func (a *Adapter) dispatch(job delivery) {
	select {
	case <-a.stopped:
		return
	default:
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		a.record(job, a.send(job))
	}()
}

func (a *Adapter) record(job delivery, outcome string) {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	_, _ = fmt.Fprintf(a.config.Audit, "channel=discord input=%s status=%s delivery=%s\n", job.inputID, job.status, outcome)
}

func (a *Adapter) send(job delivery) string {
	chunks := responseChunks(job.content, job.status, job.truncated)
	for index, content := range chunks {
		if !a.config.Now().Before(job.token.expiresAt) {
			return "expired"
		}
		method := http.MethodPost
		endpoint := a.apiBase + "/webhooks/" + url.PathEscape(a.config.ApplicationID) + "/" + url.PathEscape(job.token.token)
		if index == 0 {
			method = http.MethodPatch
			endpoint += "/messages/@original"
		}
		payload, _ := json.Marshal(map[string]any{"content": content, "allowed_mentions": allowedMentions()})
		request, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "failed"
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := a.client.Do(request)
		if err != nil {
			return "uncertain"
		}
		body, tooLarge := readBounded(response.Body)
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if response.StatusCode == http.StatusTooManyRequests {
				return "rate_limited"
			}
			return "failed"
		}
		if tooLarge || body == nil {
			return "uncertain"
		}
		var message struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &message) != nil || !validSnowflake(message.ID) {
			return "uncertain"
		}
	}
	return "completed"
}

func Run(ctx context.Context, p *project.Project, driver harness.Driver, conversation string, config Config) error {
	if p.DiscordChannel == nil {
		return errors.New("agent project does not define channels/discord.md")
	}
	if err := gateway.ValidateConversation(conversation); err != nil {
		return err
	}
	if config.Listen == "" {
		config.Listen = DefaultListen
	}
	if config.Path == "" {
		config.Path = DefaultPath
	}
	submissions := make(chan gateway.Submission, 32)
	adapter, err := New(config, submissions)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		adapter.Close()
		return fmt.Errorf("cannot start Discord loopback listener at %s; choose an unused loopback port: %w", config.Listen, err)
	}
	_, _ = fmt.Fprintf(adapter.config.Diagnostics, "Discord channel ready at http://%s%s\n", listener.Addr().String(), config.Path)
	server := &http.Server{
		Handler:           adapter.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	gatewayDone := make(chan error, 1)
	go func() {
		gatewayDone <- gateway.RunSubmissions(child, p, driver, conversation, submissions, func(event gateway.Event) error {
			adapter.HandleEvent(event)
			return nil
		})
	}()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()

	var result error
	gatewayFinished := false
	select {
	case result = <-gatewayDone:
		gatewayFinished = true
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("discord listener failed")
		}
	case <-ctx.Done():
		result = ctx.Err()
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
	_ = server.Shutdown(shutdown)
	stop()
	adapter.Stop()
	if !gatewayFinished {
		<-gatewayDone
	}
	adapter.Close()
	return result
}

func commandMessage(incoming interaction) (string, bool) {
	if len(incoming.Data.Options) != 1 {
		return "", false
	}
	option := incoming.Data.Options[0]
	if option.Name != "message" || option.Type != 3 {
		return "", false
	}
	var message string
	if json.Unmarshal(option.Value, &message) != nil || message == "" || strings.TrimSpace(message) == "" || !utf8.ValidString(message) || len([]byte(message)) > maxMessageBytes {
		return "", false
	}
	return message, true
}

func validSnowflake(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number != 0
}

func validToken(value string) bool {
	if len(value) == 0 || len(value) > maxTokenBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validateAPIBase(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("discord API endpoint is invalid")
	}
	if parsed.Scheme != "https" {
		host, _, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr != nil {
			host = parsed.Host
		}
		ip := net.ParseIP(host)
		if parsed.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("discord API endpoint must use HTTPS")
		}
	}
	return parsed, nil
}

func terminalStatus(event gateway.Event) string {
	if strings.HasPrefix(event.Type, "turn.") {
		status := strings.TrimPrefix(event.Type, "turn.")
		if terminalOutcome(status) {
			return status
		}
	}
	if event.Type == "driver.process_failed" && event.InputID != "" {
		return "failed"
	}
	return ""
}

func terminalOutcome(status string) bool {
	return status == "completed" || status == "failed" || status == "uncertain"
}

func appendBounded(current *turn, value string) {
	if current.truncated || value == "" {
		return
	}
	remaining := maxOutputRunes - current.runes
	for index := range value {
		if remaining == 0 {
			current.output.WriteString(value[:index])
			current.truncated = true
			return
		}
		remaining--
		current.runes++
	}
	current.output.WriteString(value)
}

func responseChunks(content, status string, truncated bool) []string {
	if content == "" {
		switch status {
		case "completed":
			content = "Agent turn completed."
		case "uncertain":
			content = "Agent turn outcome is uncertain and was not retried."
		default:
			content = "Agent turn failed."
		}
	} else if status == "failed" || status == "uncertain" {
		content += "\n\nAgent turn " + status + "."
	}
	if truncated {
		content += "\n\n[output truncated]"
	}
	runes := []rune(content)
	chunks := make([]string, 0, maxChunks)
	for len(runes) > 0 && len(chunks) < maxChunks {
		count := min(len(runes), maxChunkRunes)
		chunks = append(chunks, string(runes[:count]))
		runes = runes[count:]
	}
	return chunks
}

func allowedMentions() map[string]any { return map[string]any{"parse": []string{}} }

func immediateMessage(content string) map[string]any {
	return map[string]any{"type": 4, "data": map[string]any{"content": content, "flags": 64, "allowed_mentions": allowedMentions()}}
}

func rejectionText(status string) string {
	if status == "queue_full" {
		return "Agent queue is full. Try again later."
	}
	return "Agent command was rejected."
}

func writeJSON(response http.ResponseWriter, status int, value any) error {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	return json.NewEncoder(response).Encode(value)
}

func readBounded(reader io.Reader) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, false
	}
	return data, len(data) > maxResponseBytes
}
