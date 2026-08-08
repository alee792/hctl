package adapterhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"hctl/channeladapter"
	"hctl/internal/channel/controller"
	"hctl/internal/dispatch"
	"hctl/internal/harness/codex"
	"hctl/internal/integration"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/secureenv"
)

type testEncoder struct {
	runtime     *Runtime
	disposition channeladapter.EffectDisposition
	writeErr    error
	mu          sync.Mutex
	frames      []channeladapter.Envelope
	maximum     int
}

func (encoder *testEncoder) SetMaxFrameBytes(maximum int) error {
	encoder.maximum = maximum
	return nil
}
func (encoder *testEncoder) Write(frame channeladapter.Envelope, _ channeladapter.Direction) error {
	encoder.mu.Lock()
	encoder.frames = append(encoder.frames, frame)
	encoder.mu.Unlock()
	if encoder.writeErr != nil {
		return encoder.writeErr
	}
	switch payload := frame.Payload.(type) {
	case channeladapter.InteractionRequest:
		disposition := encoder.disposition
		if disposition == "" {
			disposition = channeladapter.EffectExact
		}
		failure := channeladapter.Failure{}
		if disposition == channeladapter.EffectFailed {
			failure = channeladapter.Failure{Class: channeladapter.DiagnosticConnection, Code: "render_failed"}
		}
		encoder.runtime.routeResponse(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.receipt.1", CorrelationID: frame.ID, Payload: &channeladapter.InteractionReceipt{InteractionID: payload.InteractionID, Disposition: disposition, Failure: failure}})
	case channeladapter.Delivery:
		disposition := encoder.disposition
		if disposition == "" {
			disposition = channeladapter.EffectExact
		}
		encoder.runtime.routeResponse(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.delivery.1", CorrelationID: frame.ID, Payload: &channeladapter.DeliveryResult{Disposition: disposition}})
	}
	return nil
}

func (encoder *testEncoder) containsCancel() bool {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	for _, frame := range encoder.frames {
		if _, ok := frame.Payload.(channeladapter.InteractionCancel); ok {
			return true
		}
	}
	return false
}

type testController struct {
	done        chan struct{}
	runtime     *Runtime
	submit      func(controller.Inbound)
	pending     interaction.PendingInteraction
	hasPending  bool
	acceptErr   error
	disposition interaction.AnswerDisposition
	continued   atomic.Int32
	submitted   atomic.Int32
	reset       atomic.Int32
	closeBlock  <-chan struct{}
}

func (test *testController) Submit(_ context.Context, incoming controller.Inbound) (dispatch.SubmissionResult, error) {
	test.submitted.Add(1)
	if test.submit != nil {
		test.submit(incoming)
	}
	return dispatch.SubmissionResult{Status: "active"}, nil
}
func (test *testController) Status(string) controller.Status {
	return controller.Status{Conversation: dispatch.ConversationStatus{State: dispatch.LifecycleInactive}, Capacity: dispatch.CapacityStatus{ActiveLimit: 1, ResidentLimit: 1}}
}
func (test *testController) Reset(string, string) error { test.reset.Add(1); return nil }
func (test *testController) AcceptInteraction(string, string, interaction.AnswerAttempt) (interaction.AnswerDisposition, error) {
	disposition := test.disposition
	if disposition == "" {
		disposition = interaction.AnswerAccepted
	}
	return disposition, test.acceptErr
}
func (test *testController) ContinueInteraction(string) error { test.continued.Add(1); return nil }
func (test *testController) PendingInteraction(string, string) (interaction.PendingInteraction, bool, error) {
	return test.pending, test.hasPending, nil
}
func (test *testController) RenderInteraction(string, string) (bool, error) { return false, nil }
func (test *testController) Done() <-chan struct{} {
	if test.done == nil {
		test.done = make(chan struct{})
	}
	return test.done
}
func (test *testController) Err() error { return nil }
func (test *testController) Close() {
	if test.closeBlock != nil {
		<-test.closeBlock
	}
}

func regressionRuntime(maximum int) (*Runtime, *testEncoder, *testController) {
	limits := channeladapter.Limits{MaxFrameBytes: 4096, MaxTextBytes: 16, MaxAttachments: 1, MaxAttachmentBytes: 8, MaxOutstanding: maximum}
	controlled := &testController{}
	runtime := &Runtime{
		config: Config{after: time.After, Audit: io.Discard}, controller: controlled,
		features: []channeladapter.Feature{channeladapter.FeatureInteractiveComponents}, limits: limits,
		surfaces:    map[string]channeladapter.Surface{"route_1": {Route: channeladapter.Route{Handle: "route_1"}, ConversationID: "conversation-1", Kind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)}},
		outstanding: make(chan struct{}, maximum), writeGate: make(chan struct{}, 1), pending: map[string]chan channeladapter.Envelope{}, interactions: map[string]interactionTarget{}, targets: map[string]target{}, events: map[string]eventReceipt{}, done: make(chan struct{}),
	}
	encoder := &testEncoder{runtime: runtime}
	runtime.encoder = encoder
	return runtime, encoder, controlled
}

func confirmInteraction() interaction.Request {
	return interaction.Request{SchemaVersion: 1, Kind: interaction.KindConfirm, Prompt: "Continue?", Policy: interaction.Policy{ExpiresAfterSeconds: 60, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "confirmation", Kind: interaction.KindConfirm, Label: "Continue", Required: true}}
}

func TestNegotiatedLimitsBoundSemanticsAndOutstandingAdmission(t *testing.T) {
	runtime, _, _ := regressionRuntime(1)
	message := channeladapter.InboundMessage{SourceID: "source-1", Route: channeladapter.Route{Handle: "route_1"}, ConversationID: "conversation-1", SurfaceKind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64), Message: channeladapter.MessageRef{Handle: "message_1"}, Author: channeladapter.Author{Handle: "author_1"}, Text: strings.Repeat("x", 17)}
	if err := runtime.validateInboundLimits(message); err == nil {
		t.Fatal("narrowed inbound text limit was not enforced")
	}
	message.Text = "ok"
	message.Attachments = []channeladapter.AttachmentDescriptor{{Handle: "attachment_1", Name: "a", Size: 9}}
	if err := runtime.validateInboundLimits(message); err == nil {
		t.Fatal("narrowed attachment limit was not enforced")
	}
	runtime.outstanding <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runtime.acquireOutstanding(ctx, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded outstanding admission = %v", err)
	}
	runtime.releaseOutstanding()
	if err := runtime.registerSurface(channeladapter.Surface{Route: channeladapter.Route{Handle: "route_2"}, ConversationID: "conversation-2", Kind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("c", 64), PrincipalKey: strings.Repeat("d", 64)}); err == nil {
		t.Fatal("dynamic surface admission exceeded negotiated capacity")
	}
}

func TestInteractionReceiptAnswerCancellationAndErrorRetireCapacity(t *testing.T) {
	runtime, encoder, controlled := regressionRuntime(1)
	message := channeladapter.MessageRef{Handle: "message_1"}
	if err := runtime.remember("input-1", target{route: channeladapter.Route{Handle: "route_1"}, message: &message}); err != nil {
		t.Fatal(err)
	}
	intent := interaction.RenderIntent{InteractionID: "interaction.1", InputID: "input-1", Request: confirmInteraction()}
	if outcome := runtime.Render(context.Background(), intent); outcome != interaction.EffectSucceeded || len(runtime.interactions) != 1 || len(runtime.outstanding) != 1 {
		t.Fatalf("render outcome=%s interactions=%d outstanding=%d", outcome, len(runtime.interactions), len(runtime.outstanding))
	}
	correlation := interactionFrameID(intent.InteractionID)
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerSubmit, Fields: []channeladapter.FieldAnswer{{FieldID: "confirmation", Confirmed: boolAddress(true)}}}
	frame := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.answer.1", CorrelationID: correlation, Payload: &channeladapter.InteractionResult{InteractionID: intent.InteractionID, Answer: answer}}
	if err := runtime.interactionResult(frame, *frame.Payload.(*channeladapter.InteractionResult)); err != nil {
		t.Fatal(err)
	}
	if len(runtime.interactions) != 0 || len(runtime.outstanding) != 0 || controlled.continued.Load() != 1 {
		t.Fatal("accepted answer did not retire correlation and continue")
	}

	encoder.disposition = channeladapter.EffectAmbiguous
	intent.InteractionID = "interaction.2"
	if outcome := runtime.Render(context.Background(), intent); outcome != interaction.EffectUncertain || len(runtime.interactions) != 0 || len(runtime.outstanding) != 0 || !encoder.containsCancel() {
		t.Fatalf("ambiguous render outcome=%s interactions=%d outstanding=%d cancel=%t", outcome, len(runtime.interactions), len(runtime.outstanding), encoder.containsCancel())
	}
	encoder.disposition, encoder.writeErr = channeladapter.EffectExact, errors.New("write failed")
	intent.InteractionID = "interaction.3"
	if outcome := runtime.Render(context.Background(), intent); outcome != interaction.EffectUncertain || len(runtime.interactions) != 0 || len(runtime.outstanding) != 0 {
		t.Fatalf("failed render outcome=%s interactions=%d outstanding=%d", outcome, len(runtime.interactions), len(runtime.outstanding))
	}
}

func TestBoundedStderrRedactsCredentialAndProtocol(t *testing.T) {
	secret := "adapter-secret-value"
	var output bytes.Buffer
	writer := newDiagnosticWriter(&output, []string{"HCTL_DISCORD_TOKEN=" + secret})
	var violated atomic.Bool
	writer.onProtocolViolation = func() { violated.Store(true) }
	for _, fragment := range []string{"failure token=adapter-", "secret-value\n{\"proto", "col_version\":1,\"pay", `load":{"token":"adapter-secret-value"}}`} {
		_, _ = writer.Write([]byte(fragment))
	}
	writer.Flush()
	if got := output.String(); len(got) > channeladapter.MaxStderrBytes || strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") || !strings.Contains(got, "protocol-like stderr redacted") || !violated.Load() {
		t.Fatalf("safe stderr = %q", got)
	}
	output.Reset()
	writer = newDiagnosticWriter(&output, []string{"HCTL_DISCORD_TOKEN=" + secret})
	_, _ = writer.Write([]byte("partial adapter-sec"))
	writer.Flush()
	if got := output.String(); strings.Contains(got, "adapter-sec") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("partial credential stderr = %q", got)
	}
}

func TestSanitizedStderrOutputCapIncludesPrefixes(t *testing.T) {
	var output bytes.Buffer
	writer := newDiagnosticWriter(&output, nil)
	for index := 0; index < channeladapter.MaxStderrBytes; index++ {
		_, _ = writer.Write([]byte("x\n"))
	}
	writer.Flush()
	if output.Len() != channeladapter.MaxStderrBytes {
		t.Fatalf("sanitized stderr bytes = %d", output.Len())
	}
}

func TestStartupReplayQueueAndReplyTargetsFailWithoutEviction(t *testing.T) {
	frame := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.connection.1", Payload: channeladapter.Connection{State: channeladapter.ConnectionReady}}
	queue := frameQueue{}
	for index := 0; index < channeladapter.MaxQueuedFrames; index++ {
		frame.ID = fmt.Sprintf("adapter.connection.%d", index+1)
		if err := queue.add(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.add(frame); err == nil {
		t.Fatal("startup replay frame ceiling was not enforced")
	}
	queue = frameQueue{bytes: channeladapter.MaxQueuedBytes}
	if err := queue.add(frame); err == nil {
		t.Fatal("startup replay byte ceiling was not enforced")
	}

	runtime, encoder, controlled := regressionRuntime(1)
	first := target{route: channeladapter.Route{Handle: "route_1"}}
	if err := runtime.remember("input-1", first); err != nil {
		t.Fatal(err)
	}
	if err := runtime.remember("input-2", target{route: channeladapter.Route{Handle: "route_2"}}); err == nil || runtime.targets["input-1"].route.Handle != "route_1" {
		t.Fatal("reply-target saturation silently evicted accepted work")
	}
	incoming := channeladapter.InboundMessage{SourceID: "input-2", Route: channeladapter.Route{Handle: "route_1"}, ConversationID: "conversation-1", SurfaceKind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64), Message: channeladapter.MessageRef{Handle: "message_2"}, Author: channeladapter.Author{Handle: "author_1"}, Text: "hello"}
	envelope := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.inbound.capacity", Payload: incoming}
	if err := runtime.inbound(envelope, incoming); err != nil {
		t.Fatal(err)
	}
	if controlled.submitted.Load() != 0 || len(encoder.frames) == 0 || encoder.frames[len(encoder.frames)-1].Payload.(channeladapter.EventAck).Disposition != "rejected" {
		t.Fatal("saturated reply target was not rejected before controller admission")
	}
	runtime.forget("input-1")
	if err := runtime.remember("input-2", target{route: incoming.Route}); err != nil {
		t.Fatal("released reply-target capacity was not reusable")
	}
}

func TestOperationModesPreserveHumanInputAndCancelTheProcessTree(t *testing.T) {
	if setupOperationTimeout <= channeladapter.CommandTimeout {
		t.Fatal("interactive setup retained the ordinary command deadline")
	}
	script := filepath.Join(t.TempDir(), "adapter")
	writeFile(t, script, `#!/bin/sh
operation="$1"
status=ready
if [ "$operation" = status ]; then
  if IFS= read -r unexpected; then exit 9; fi
elif ! IFS= read -r answer || [ "$answer" != trusted-input ]; then
  exit 8
fi
[ "$operation" = remove ] && status=removed
printf '{"schema_version":1,"operation":"%s","profile_id":"default","status":"%s","message":"ok"}\n' "$operation" "$status"
`, 0o755)
	for _, mode := range []integration.ChannelAdapterMode{integration.ChannelAdapterSetup, integration.ChannelAdapterRemove, integration.ChannelAdapterStatus} {
		result, err := RunOperation(context.Background(), mode, Launch{Command: script, Arguments: []string{string(mode), "--profile", "default"}, WorkingDirectory: filepath.Dir(script)}, AdapterEnvironment(""), strings.NewReader("trusted-input\n"), io.Discard)
		if err != nil || result.Operation != string(mode) {
			t.Fatalf("%s operation terminal contract = %#v, %v", mode, result, err)
		}
	}

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	writeFile(t, script, "#!/bin/sh\nsleep 60 &\nprintf '%s' \"$!\" > \"$HCTL_CHILD_PID\"\nwait\n", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := RunOperation(ctx, integration.ChannelAdapterSetup, Launch{Command: script, Arguments: []string{"setup", "--profile", "default"}, WorkingDirectory: filepath.Dir(script)}, secureenv.Replace(AdapterEnvironment(""), map[string]string{"HCTL_CHILD_PID": pidFile}), strings.NewReader(""), io.Discard)
		done <- err
	}()
	waitForFile(t, pidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("operation cancellation = %v", err)
		}
	case <-time.After(channeladapter.ForcedExitTimeout + time.Second):
		t.Fatal("cancelled operation did not stop")
	}
	waitFor(t, func() bool { return errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH) }, "cancelled operation child cleanup")
}

type stuckProcess struct{ kills atomic.Int32 }

func (*stuckProcess) Input() io.WriteCloser { return nopWriteCloser{io.Discard} }
func (*stuckProcess) Output() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (*stuckProcess) Done() <-chan error    { return make(chan error) }
func (process *stuckProcess) KillTree()     { process.kills.Add(1) }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestForcedCleanupAndControllerCloseRemainBounded(t *testing.T) {
	runtime, _, controlled := regressionRuntime(1)
	process := &stuckProcess{}
	runtime.process = process
	block := make(chan struct{})
	controlled.closeBlock = block
	runtime.config.after = func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- time.Now()
		return ready
	}
	started := time.Now()
	runtime.forceClose()
	runtime.closeControllerBounded()
	if time.Since(started) > time.Second || process.kills.Load() == 0 {
		t.Fatalf("forced cleanup elapsed=%s kills=%d", time.Since(started), process.kills.Load())
	}
	close(block)
}

func TestExternalAdapterInteractionsControlsRecoveryAndFailures(t *testing.T) {
	if os.Getenv("HCTL_FAKE_ADAPTER") == "1" {
		t.Skip("parent-only test")
	}
	t.Run("interaction answer", func(t *testing.T) {
		controlled := &testController{done: make(chan struct{})}
		outcomes := make(chan interaction.EffectOutcome, 1)
		controlled.submit = func(incoming controller.Inbound) {
			go func() {
				outcomes <- controlled.runtime.Render(context.Background(), interaction.RenderIntent{InteractionID: "interaction.external", InputID: incoming.InputID, Request: confirmInteraction()})
			}()
		}
		_, cancel, done, _ := startExternalRuntime(t, "interaction", controlled)
		defer stopExternalRuntime(t, cancel, done)
		select {
		case outcome := <-outcomes:
			if outcome != interaction.EffectSucceeded {
				t.Fatalf("render outcome = %s", outcome)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("external adapter did not acknowledge interaction")
		}
		waitFor(t, func() bool { return controlled.continued.Load() == 1 }, "external interaction answer")
	})

	t.Run("interaction cancellation", func(t *testing.T) {
		controlled := &testController{done: make(chan struct{})}
		controlled.submit = func(incoming controller.Inbound) {
			go controlled.runtime.Render(context.Background(), interaction.RenderIntent{InteractionID: "interaction.external", InputID: incoming.InputID, Request: confirmInteraction()})
		}
		_, cancel, done, _ := startExternalRuntime(t, "interaction_cancel", controlled)
		defer stopExternalRuntime(t, cancel, done)
		waitFor(t, func() bool { return controlled.continued.Load() == 1 }, "external interaction cancellation")
	})

	t.Run("status and reset", func(t *testing.T) {
		controlled := &testController{done: make(chan struct{})}
		_, cancel, done, _ := startExternalRuntime(t, "controls", controlled)
		defer stopExternalRuntime(t, cancel, done)
		waitFor(t, func() bool { return controlled.reset.Load() == 1 }, "external reset")
	})

	t.Run("ambiguous delivery", func(t *testing.T) {
		controlled := &testController{done: make(chan struct{})}
		results := make(chan error, 1)
		controlled.submit = func(incoming controller.Inbound) {
			go func() {
				results <- controlled.runtime.Deliver(controller.Outcome{InputID: incoming.InputID, Target: incoming.Target, Parts: []string{"reply"}})
			}()
		}
		_, cancel, done, _ := startExternalRuntime(t, "ambiguous", controlled)
		defer stopExternalRuntime(t, cancel, done)
		select {
		case err := <-results:
			if err == nil || !strings.Contains(err.Error(), "uncertain") {
				t.Fatalf("ambiguous delivery error = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("external ambiguous delivery did not complete")
		}
	})

	t.Run("delivered interaction recovery", func(t *testing.T) {
		controlled := &testController{done: make(chan struct{}), hasPending: true, pending: interaction.PendingInteraction{
			InteractionID: "interaction.recovered", InputID: "input.recovered", Request: confirmInteraction(), ExpiresAt: time.Now().Add(time.Minute), Delivery: interaction.DeliveryDelivered,
		}}
		_, cancel, done, marker := startExternalRuntime(t, "recovery", controlled)
		defer stopExternalRuntime(t, cancel, done)
		waitForFile(t, marker)
		waitFor(t, func() bool { return controlled.submitted.Load() == 1 }, "post-recovery adapter replay")
	})

	for _, scenario := range []string{"bounds", "child_failure"} {
		t.Run(scenario, func(t *testing.T) {
			controlled := &testController{done: make(chan struct{})}
			running, err := newExternalRuntime(t, scenario, controlled, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := running.Run(ctx); err == nil {
				t.Fatalf("%s adapter failure was accepted", scenario)
			}
		})
	}
}

func startExternalRuntime(t *testing.T, scenario string, controlled *testController) (*Runtime, context.CancelFunc, <-chan error, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "marker")
	running, err := newExternalRuntime(t, scenario, controlled, marker)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- running.Run(ctx) }()
	return running, cancel, done, marker
}

func newExternalRuntime(t *testing.T, scenario string, controlled *testController, marker string) (*Runtime, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	environment := secureenv.Replace(AdapterEnvironment(""), map[string]string{"HCTL_FAKE_ADAPTER": "1", "HCTL_FAKE_SCENARIO": scenario, "HCTL_FAKE_MARKER": marker})
	return New(Config{
		Project: &project.Project{Name: "fixture"}, Driver: codex.New(executable), ProfileID: "default", Environment: environment,
		Launch:      Launch{Command: executable, Arguments: []string{"-test.run=^TestFakeAdapterProcess$"}, WorkingDirectory: filepath.Dir(executable), PackageID: "fixture-channel@1.0.0", CapabilityID: "fixture", ChannelKind: "fixture", ProtocolMinimum: 1, ProtocolBefore: 2, Features: []integration.ChannelFeature{"typing", "replies", "interactive-components", "text-fallback"}},
		TurnTimeout: time.Second, IdleTimeout: time.Minute, MaxResident: 2, MaxActive: 1, Executable: executable, Audit: io.Discard,
		newController: func(_ context.Context, _ controller.Config, delivery controller.Delivery) (channelController, error) {
			controlled.runtime = delivery.(*Runtime)
			return controlled, nil
		},
	})
}

func stopExternalRuntime(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("external adapter shutdown: %v", err)
		}
	case <-time.After(channeladapter.ShutdownTimeout + channeladapter.ForcedExitTimeout + time.Second):
		t.Error("external adapter shutdown exceeded bound")
	}
}

func waitFor(t *testing.T, ready func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		return
	}
	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil }, "external adapter marker")
}

func boolAddress(value bool) *bool { return &value }
