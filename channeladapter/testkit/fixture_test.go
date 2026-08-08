package testkit

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	protocol "hctl/channeladapter"
)

type duplex struct {
	read  *io.PipeReader
	write *io.PipeWriter
}

func (value duplex) Read(data []byte) (int, error)  { return value.read.Read(data) }
func (value duplex) Write(data []byte) (int, error) { return value.write.Write(data) }

func startFixture(t *testing.T, fixture Fixture) (duplex, <-chan error) {
	t.Helper()
	hostToAdapterReader, hostToAdapterWriter := io.Pipe()
	adapterToHostReader, adapterToHostWriter := io.Pipe()
	errors := make(chan error, 1)
	go func() {
		errors <- fixture.Run(context.Background(), hostToAdapterReader, adapterToHostWriter)
		_ = adapterToHostWriter.Close()
		_ = hostToAdapterReader.Close()
	}()
	return duplex{read: adapterToHostReader, write: hostToAdapterWriter}, errors
}

func initialize(t *testing.T, connection duplex) (*protocol.Decoder, *protocol.Encoder) {
	t.Helper()
	decoder := protocol.NewDecoder(connection)
	encoder := protocol.NewEncoder(connection)
	helloFrame, err := decoder.Read(protocol.FromAdapter)
	if err != nil {
		t.Fatal(err)
	}
	hello := helloFrame.Payload.(*protocol.Hello)
	init := protocol.Initialize{SelectedVersion: 1, ProfileID: "default", Features: hello.Features, Limits: hello.Limits, Policy: protocol.RuntimePolicy{Participation: protocol.ParticipationAmbient, MaxInboundTextBytes: protocol.MaxTextBytes, MaxDeliveryTextBytes: protocol.MaxTextBytes, MaxAttachmentBytes: protocol.MaxAttachmentBytes}}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.initialize.1", CorrelationID: helloFrame.ID, Payload: init}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	ready, err := decoder.Read(protocol.FromAdapter)
	if err != nil {
		t.Fatal(err)
	}
	if ready.CorrelationID != "host.initialize.1" {
		t.Fatalf("ready = %#v", ready)
	}
	return decoder, encoder
}

func readFrame(t *testing.T, decoder *protocol.Decoder) protocol.Envelope {
	t.Helper()
	frame, err := decoder.Read(protocol.FromAdapter)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestDeterministicFixtureCoversRuntimeJourney(t *testing.T) {
	connection, done := startFixture(t, &Deterministic{})
	decoder, encoder := initialize(t, connection)
	connected := readFrame(t, decoder)
	if connected.Payload.(*protocol.Connection).State != protocol.ConnectionReady {
		t.Fatalf("connection = %#v", connected)
	}
	inbound := readFrame(t, decoder)
	if inbound.Payload.(*protocol.InboundMessage).Text != "hello" {
		t.Fatalf("inbound = %#v", inbound)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.ack.1", CorrelationID: inbound.ID, Payload: protocol.EventAck{Disposition: "accepted"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	reconnecting := readFrame(t, decoder)
	reconnected := readFrame(t, decoder)
	if reconnecting.Payload.(*protocol.Connection).State != protocol.ConnectionReconnecting || reconnected.Payload.(*protocol.Connection).State != protocol.ConnectionReady {
		t.Fatal("reconnect lifecycle missing")
	}
	replay := readFrame(t, decoder)
	if replay.ID != inbound.ID || replay.Payload.(*protocol.InboundMessage).SourceID != inbound.Payload.(*protocol.InboundMessage).SourceID {
		t.Fatalf("replay changed stable correlation: first=%#v replay=%#v", inbound, replay)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.ack.2", CorrelationID: replay.ID, Payload: protocol.EventAck{Disposition: "duplicate"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	statusRequest := readFrame(t, decoder)
	if statusRequest.Payload.(*protocol.ControlRequest).Action != protocol.ControlStatus {
		t.Fatalf("status request = %#v", statusRequest)
	}
	status := &protocol.RuntimeStatus{Agent: "fixture-agent", Harness: "codex", State: protocol.LifecycleIdle, ActiveLimit: 2, Resident: 1, ResidentLimit: 4}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.control.status.1", CorrelationID: statusRequest.ID, Payload: protocol.ControlResult{Action: protocol.ControlStatus, Disposition: protocol.ControlExact, Status: status}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	resetRequest := readFrame(t, decoder)
	if resetRequest.Payload.(*protocol.ControlRequest).Action != protocol.ControlReset {
		t.Fatalf("reset request = %#v", resetRequest)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.control.reset.1", CorrelationID: resetRequest.ID, Payload: protocol.ControlResult{Action: protocol.ControlReset, Disposition: protocol.ControlBusy}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.delivery.1", Payload: protocol.Delivery{Action: protocol.DeliverySend, Route: protocol.Route{Handle: "route_1"}, ReplyTo: &protocol.MessageRef{Handle: "message_1"}, Text: "reply"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	delivered := readFrame(t, decoder)
	if delivered.Payload.(*protocol.DeliveryResult).Disposition != protocol.EffectExact {
		t.Fatalf("delivery = %#v", delivered)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.delivery.2", Payload: protocol.Delivery{Action: protocol.DeliverySend, Route: protocol.Route{Handle: "route_1"}, Text: "second"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	ambiguous := readFrame(t, decoder)
	if ambiguous.Payload.(*protocol.DeliveryResult).Disposition != protocol.EffectAmbiguous {
		t.Fatalf("ambiguous = %#v", ambiguous)
	}
	request := protocol.SemanticInteractionRequest{SchemaVersion: 1, Kind: protocol.InteractionConfirm, Prompt: "Continue?", Policy: protocol.InteractionPolicy{ExpiresAfterSeconds: 60, Cancellation: protocol.CancellationAllowed}, Field: &protocol.Field{ID: "confirmation", Kind: protocol.InteractionConfirm, Label: "Continue", Required: true}}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.interaction.1", Payload: protocol.InteractionRequest{InteractionID: "interaction.1", Route: protocol.Route{Handle: "route_1"}, ReplyTo: protocol.MessageRef{Handle: "message_1"}, Request: request}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	receipt := readFrame(t, decoder)
	if receipt.Payload.(*protocol.InteractionReceipt).Disposition != protocol.EffectExact {
		t.Fatalf("interaction receipt = %#v", receipt)
	}
	answer := readFrame(t, decoder)
	if answer.Payload.(*protocol.InteractionResult).Answer.Action != protocol.AnswerSubmit {
		t.Fatalf("answer = %#v", answer)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.cancel.1", Payload: protocol.InteractionCancel{InteractionID: "interaction.2"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.fetch.1", Payload: protocol.AttachmentFetch{TransferID: "transfer.1", AttachmentHandle: "attachment_1", MaximumBytes: 1024}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	chunk := readFrame(t, decoder)
	if !chunk.Payload.(*protocol.AttachmentChunk).Final {
		t.Fatalf("chunk = %#v", chunk)
	}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.shutdown.1", Payload: protocol.Shutdown{Reason: "complete"}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	shutdown := readFrame(t, decoder)
	if shutdown.CorrelationID != "host.shutdown.1" {
		t.Fatalf("shutdown = %#v", shutdown)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fixture did not stop")
	}
}

func TestNoopFixtureImplementsSameContract(t *testing.T) {
	connection, done := startFixture(t, Noop{})
	decoder, encoder := initialize(t, connection)
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: "host.shutdown.1", Payload: protocol.Shutdown{}}, protocol.FromHost); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Read(protocol.FromAdapter); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFixtureFailsClosedOnMalformedAndOversizedFrames(t *testing.T) {
	for _, content := range []string{"not-json\n", strings.Repeat("x", protocol.MaxFrameBytes+1) + "\n"} {
		t.Run(string(content[0]), func(t *testing.T) {
			connection, done := startFixture(t, Noop{})
			decoder := protocol.NewDecoder(connection)
			if _, err := decoder.Read(protocol.FromAdapter); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(connection, content); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("malformed frame accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("fixture did not fail")
			}
		})
	}
}
