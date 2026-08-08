package channeladapter

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestInteractionValidationMatchesCanonicalBounds(t *testing.T) {
	t.Parallel()
	valid := func() SemanticInteractionRequest {
		return SemanticInteractionRequest{
			SchemaVersion: 1,
			Kind:          InteractionChooseMany,
			Prompt:        "Choose one or more\nInternal tabs\tare allowed.",
			FallbackText:  "Reply with selections.",
			Policy:        InteractionPolicy{ExpiresAfterSeconds: 60, Cancellation: CancellationAllowed},
			Field: &Field{
				ID: "choices", Kind: InteractionChooseMany, Label: "Choices", Description: "Available choices", Required: true,
				Options:       []Option{{ID: "first", Label: "First", Description: "First choice", Value: "first-value"}, {ID: "second", Label: "Second", Value: "second-value"}},
				MinSelections: 1, MaxSelections: 2,
			},
		}
	}
	if err := validateSemanticRequest(valid()); err != nil {
		t.Fatalf("canonical request: %v", err)
	}
	tooMany := valid()
	tooMany.Field.MaxSelections = 3
	if err := validateSemanticRequest(tooMany); err == nil || !strings.Contains(err.Error(), "selection bounds") {
		t.Fatalf("choose-many cardinality error = %v", err)
	}
	withFreeform := valid()
	withFreeform.Field.AllowFreeform = true
	withFreeform.Field.MinLength = 0
	withFreeform.Field.MaxLength = 4000
	withFreeform.Field.MaxSelections = 3
	if err := validateSemanticRequest(withFreeform); err != nil {
		t.Fatalf("options plus freeform cardinality: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SemanticInteractionRequest)
	}{
		{name: "prompt leading space", mutate: func(request *SemanticInteractionRequest) { request.Prompt = " prompt" }},
		{name: "prompt control", mutate: func(request *SemanticInteractionRequest) { request.Prompt = "prompt\x01" }},
		{name: "fallback trailing space", mutate: func(request *SemanticInteractionRequest) { request.FallbackText = "fallback " }},
		{name: "label newline", mutate: func(request *SemanticInteractionRequest) { request.Field.Label = "choice\nlabel" }},
		{name: "description leading space", mutate: func(request *SemanticInteractionRequest) { request.Field.Description = " description" }},
		{name: "option label trailing space", mutate: func(request *SemanticInteractionRequest) { request.Field.Options[0].Label = "First " }},
		{name: "option description control", mutate: func(request *SemanticInteractionRequest) { request.Field.Options[0].Description = "first\x7f" }},
		{name: "option value tab", mutate: func(request *SemanticInteractionRequest) { request.Field.Options[0].Value = "first\tvalue" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			test.mutate(&request)
			if err := validateSemanticRequest(request); err == nil {
				t.Fatal("non-canonical text was accepted")
			}
		})
	}
}

func TestInteractionFreeformAnswerAllowsCoreNormalization(t *testing.T) {
	t.Parallel()
	freeform := "  operator supplied value\n"
	result := InteractionResult{
		InteractionID: "interaction.1",
		Answer: SemanticInteractionAnswer{
			SchemaVersion: 1,
			Action:        AnswerSubmit,
			Fields:        []FieldAnswer{{FieldID: "choices", Freeform: &freeform}},
		},
	}
	frame := Envelope{
		ProtocolVersion: ProtocolVersion,
		ID:              "adapter.interaction.1",
		CorrelationID:   "host.interaction.1",
		Payload:         result,
	}
	if _, err := MarshalFrame(frame, FromAdapter); err != nil {
		t.Fatalf("raw freeform answer for core normalization: %v", err)
	}
}

func TestDiagnosticLimitCoversCompletePayload(t *testing.T) {
	t.Parallel()
	diagnostic := Diagnostic{Class: DiagnosticProtocol, Severity: SeverityWarning, Code: "bounded", Message: ""}
	empty, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic.Message = strings.Repeat("a", MaxDiagnosticBytes-len(empty))
	exact, err := json.Marshal(diagnostic)
	if err != nil || len(exact) != MaxDiagnosticBytes {
		t.Fatalf("exact diagnostic payload size = %d, %v", len(exact), err)
	}
	frame := Envelope{ProtocolVersion: 1, ID: "adapter.diagnostic.exact", Payload: diagnostic}
	if _, err := MarshalFrame(frame, FromAdapter); err != nil {
		t.Fatalf("exact diagnostic payload: %v", err)
	}
	diagnostic.Message += "a"
	frame.Payload = diagnostic
	if _, err := MarshalFrame(frame, FromAdapter); err == nil || !strings.Contains(err.Error(), "diagnostic payload exceeds") {
		t.Fatalf("oversized diagnostic payload error = %v", err)
	}
}

func TestTypedNilPayloadsFailWithoutPanicking(t *testing.T) {
	t.Parallel()
	payloads := []Payload{
		(*Hello)(nil),
		(*Initialize)(nil),
		(*Ready)(nil),
		(*InboundMessage)(nil),
		(*ControlRequest)(nil),
		(*ControlResult)(nil),
		(*EventAck)(nil),
		(*Activity)(nil),
		(*Delivery)(nil),
		(*DeliveryResult)(nil),
		(*InteractionRequest)(nil),
		(*InteractionCancel)(nil),
		(*InteractionResult)(nil),
		(*AttachmentFetch)(nil),
		(*AttachmentChunk)(nil),
		(*AttachmentDeliver)(nil),
		(*AttachmentResult)(nil),
		(*Connection)(nil),
		(*Diagnostic)(nil),
		(*Shutdown)(nil),
		(*ShutdownComplete)(nil),
	}
	for index, payload := range payloads {
		t.Run(fmt.Sprintf("%02d-%T", index, payload), func(t *testing.T) {
			envelope := Envelope{ProtocolVersion: ProtocolVersion, ID: "host.typed-nil.1", Payload: payload}
			if err := ValidateEnvelope(envelope, FromHost); err == nil || !strings.Contains(err.Error(), "payload is missing") {
				t.Fatalf("ValidateEnvelope() error = %v", err)
			}
			if _, err := MarshalFrame(envelope, FromHost); err == nil || !strings.Contains(err.Error(), "payload is missing") {
				t.Fatalf("MarshalFrame() error = %v", err)
			}
		})
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
