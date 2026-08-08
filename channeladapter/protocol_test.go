package channeladapter

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameCodecIsClosedBoundedAndDirectional(t *testing.T) {
	t.Parallel()
	valid := Envelope{ProtocolVersion: 1, ID: "host.shutdown.1", Payload: Shutdown{Reason: "test complete"}}
	data, err := MarshalFrame(valid, FromHost)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrame(data, FromHost)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Payload.(*Shutdown).Reason != "test complete" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, err := DecodeFrame(data, FromAdapter); err == nil || !strings.Contains(err.Error(), "cannot travel") {
		t.Fatalf("direction error = %v", err)
	}
	unknown := bytes.Replace(data, []byte(`"reason":"test complete"`), []byte(`"reason":"test complete","token":"forbidden"`), 1)
	if _, err := DecodeFrame(unknown, FromHost); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	duplicate := bytes.Replace(data, []byte(`"id":"host.shutdown.1"`), []byte(`"id":"host.shutdown.1","id":"again"`), 1)
	if _, err := DecodeFrame(duplicate, FromHost); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate-key error = %v", err)
	}
	if _, err := DecodeFrame(bytes.Repeat([]byte("x"), MaxFrameBytes+1), FromHost); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversize error = %v", err)
	}
	unknownVersion := bytes.Replace(data, []byte(`"protocol_version":1`), []byte(`"protocol_version":2`), 1)
	if _, err := DecodeFrame(unknownVersion, FromHost); err == nil || !strings.Contains(err.Error(), "version 2 is unsupported") {
		t.Fatalf("unknown-version error = %v", err)
	}
}

func TestNegotiationCanOnlyNarrow(t *testing.T) {
	t.Parallel()
	limits := Limits{MaxFrameBytes: MaxFrameBytes, MaxTextBytes: MaxTextBytes, MaxAttachments: MaxAttachments, MaxAttachmentBytes: MaxAttachmentBytes, MaxOutstanding: MaxOutstanding}
	hello := Hello{ChannelKind: "fixture", Protocol: ProtocolRange{Minimum: 1, Before: 2}, Features: []Feature{FeatureTyping, FeatureReplies}, Limits: limits}
	initialize := Initialize{SelectedVersion: 1, ProfileID: "default", Features: []Feature{FeatureReplies}, Limits: limits, Policy: RuntimePolicy{Participation: ParticipationAmbient, MaxInboundTextBytes: MaxTextBytes, MaxDeliveryTextBytes: MaxTextBytes, MaxAttachmentBytes: MaxAttachmentBytes}}
	ready := Ready{ChannelKind: "fixture", Features: []Feature{FeatureReplies}, Limits: limits}
	if err := ValidateNegotiation(hello, initialize, ready); err != nil {
		t.Fatal(err)
	}
	ready.Features = append(ready.Features, FeatureEdits)
	if err := ValidateNegotiation(hello, initialize, ready); err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("widening error = %v", err)
	}
}

func TestOperationResultIsNonSecretAndClosed(t *testing.T) {
	t.Parallel()
	result := OperationResult{SchemaVersion: 1, Operation: "status", ProfileID: "default", Status: "ready", Identity: "fixture-bot", Message: "Profile is valid."}
	data, err := MarshalOperationResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOperationResult(data); err != nil {
		t.Fatal(err)
	}
	withSecretField := bytes.Replace(data, []byte("}"), []byte(`,"credential":"secret"}`), 1)
	if _, err := DecodeOperationResult(withSecretField); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("secret field error = %v", err)
	}
}

func TestSemanticFramesRejectPathsAndInvalidInteractionShapes(t *testing.T) {
	t.Parallel()
	inbound := Envelope{ProtocolVersion: 1, ID: "adapter.inbound.1", Payload: InboundMessage{SourceID: "source.1", Route: Route{Handle: "route_1"}, Message: MessageRef{Handle: "message_1"}, Author: Author{Handle: "author_1"}, Attachments: []AttachmentDescriptor{{Handle: "attachment_1", Name: "../secret", Size: 1}}}}
	if _, err := MarshalFrame(inbound, FromAdapter); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("path error = %v", err)
	}
	request := SemanticInteractionRequest{SchemaVersion: 1, Kind: InteractionConfirm, Prompt: "Continue?", Policy: InteractionPolicy{ExpiresAfterSeconds: 60, Cancellation: CancellationAllowed}, Field: &Field{ID: "confirmation", Kind: InteractionConfirm, Label: "Continue", Required: true, Options: []Option{{ID: "vendor_button", Label: "raw", Value: "raw"}}}}
	frame := Envelope{ProtocolVersion: 1, ID: "host.interaction.1", Payload: InteractionRequest{InteractionID: "interaction.1", Route: Route{Handle: "route_1"}, ReplyTo: MessageRef{Handle: "message_1"}, Request: request}}
	if _, err := MarshalFrame(frame, FromHost); err == nil || !strings.Contains(err.Error(), "inapplicable") {
		t.Fatalf("vendor-shaped interaction error = %v", err)
	}
}

func TestDeliveryActionsStaySemanticAndClosed(t *testing.T) {
	t.Parallel()
	tests := []Delivery{
		{Action: DeliverySend, Route: Route{Handle: "route_1"}, ReplyTo: &MessageRef{Handle: "message_1"}, Text: "reply"},
		{Action: DeliveryEdit, Route: Route{Handle: "route_1"}, Message: &MessageRef{Handle: "message_1"}, Text: "corrected"},
		{Action: DeliveryReaction, Route: Route{Handle: "route_1"}, Message: &MessageRef{Handle: "message_1"}, Reaction: "approved"},
	}
	for index, delivery := range tests {
		frame := Envelope{ProtocolVersion: 1, ID: "host.delivery." + string(rune('1'+index)), Payload: delivery}
		if _, err := MarshalFrame(frame, FromHost); err != nil {
			t.Fatalf("delivery %d: %v", index, err)
		}
	}
	invalid := Envelope{ProtocolVersion: 1, ID: "host.delivery.invalid", Payload: Delivery{Action: DeliveryReaction, Route: Route{Handle: "route_1"}, Message: &MessageRef{Handle: "message_1"}, Text: "vendor markup", Reaction: "approved"}}
	if _, err := MarshalFrame(invalid, FromHost); err == nil || !strings.Contains(err.Error(), "reaction delivery shape") {
		t.Fatalf("mixed reaction error = %v", err)
	}
	failed := Envelope{ProtocolVersion: 1, ID: "adapter.delivery.failed", CorrelationID: "host.delivery.1", Payload: DeliveryResult{Disposition: EffectFailed, Failure: Failure{Class: DiagnosticRateLimit, Code: "not_attempted"}}}
	if _, err := MarshalFrame(failed, FromAdapter); err != nil {
		t.Fatalf("classified pre-attempt failure: %v", err)
	}
}

func TestStatusAndResetRemainControllerOwnedSemanticControls(t *testing.T) {
	t.Parallel()
	request := Envelope{ProtocolVersion: 1, ID: "adapter.control.1", Payload: ControlRequest{SourceID: "source.status.1", Route: Route{Handle: "route_1"}, Message: MessageRef{Handle: "message_1"}, Action: ControlStatus}}
	if _, err := MarshalFrame(request, FromAdapter); err != nil {
		t.Fatal(err)
	}
	status := &RuntimeStatus{Agent: "maintainer", Harness: "codex", State: LifecycleIdle, Pending: 0, Active: 0, ActiveLimit: 2, Resident: 1, ResidentLimit: 4, Queued: 0}
	result := Envelope{ProtocolVersion: 1, ID: "host.control.1", CorrelationID: request.ID, Payload: ControlResult{Action: ControlStatus, Disposition: ControlExact, Status: status}}
	if _, err := MarshalFrame(result, FromHost); err != nil {
		t.Fatal(err)
	}
	result.Payload = ControlResult{Action: ControlReset, Disposition: ControlBusy}
	if _, err := MarshalFrame(result, FromHost); err != nil {
		t.Fatal(err)
	}
}
