// Package testkit provides deterministic, credential-free protocol fixtures.
// It contains no vendor behavior and is not a production adapter host.
package testkit

import (
	"context"
	"errors"
	"fmt"
	"io"

	protocol "hctl/channeladapter"
)

type Fixture interface {
	Run(context.Context, io.Reader, io.Writer) error
}

type Deterministic struct{ deliveryCount int }
type Noop struct{}

var defaultFeatures = []protocol.Feature{protocol.FeatureTyping, protocol.FeatureReplies, protocol.FeatureAttachments, protocol.FeatureInteractiveComponents, protocol.FeatureTextFallback}
var defaultLimits = protocol.Limits{MaxFrameBytes: protocol.MaxFrameBytes, MaxTextBytes: protocol.MaxTextBytes, MaxAttachments: protocol.MaxAttachments, MaxAttachmentBytes: protocol.MaxAttachmentBytes, MaxOutstanding: protocol.MaxOutstanding}
var defaultSurface = protocol.Surface{Route: protocol.Route{Handle: "route_1"}, ConversationID: "fixture-conversation-1", Kind: protocol.SurfaceDirect, SurfaceKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PrincipalKey: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}

func (fixture *Deterministic) Run(ctx context.Context, input io.Reader, output io.Writer) error {
	return run(ctx, input, output, true, &fixture.deliveryCount)
}

func (Noop) Run(ctx context.Context, input io.Reader, output io.Writer) error {
	count := 0
	return run(ctx, input, output, false, &count)
}

func run(ctx context.Context, input io.Reader, output io.Writer, scripted bool, deliveryCount *int) error {
	decoder := protocol.NewDecoder(input)
	encoder := protocol.NewEncoder(output)
	sequence := 0
	nextID := func(label string) string { sequence++; return fmt.Sprintf("fixture.%s.%d", label, sequence) }
	write := func(payload protocol.Payload, correlation string) error {
		return encoder.Write(protocol.Envelope{ProtocolVersion: protocol.ProtocolVersion, ID: nextID(string(payloadKind(payload))), CorrelationID: correlation, Payload: payload}, protocol.FromAdapter)
	}
	helloID := nextID("hello")
	hello := protocol.Hello{ChannelKind: "fixture", Protocol: protocol.ProtocolRange{Minimum: 1, Before: 2}, Features: defaultFeatures, Limits: defaultLimits}
	if err := encoder.Write(protocol.Envelope{ProtocolVersion: 1, ID: helloID, Payload: hello}, protocol.FromAdapter); err != nil {
		return err
	}
	initialize, err := decoder.Read(protocol.FromHost)
	if err != nil {
		return err
	}
	initPayload, ok := initialize.Payload.(*protocol.Initialize)
	if !ok || initialize.CorrelationID != helloID {
		return errors.New("fixture expected initialization correlated to hello")
	}
	ready := protocol.Ready{ChannelKind: "fixture", Features: append([]protocol.Feature(nil), initPayload.Features...), Limits: initPayload.Limits, Surfaces: []protocol.Surface{defaultSurface}}
	if err := write(ready, initialize.ID); err != nil {
		return err
	}
	var inboundEnvelope protocol.Envelope
	replayed := false
	controlStage := 0
	if scripted {
		if err := write(protocol.Connection{State: protocol.ConnectionReady, Attempt: 0}, ""); err != nil {
			return err
		}
		inboundEnvelope = protocol.Envelope{ProtocolVersion: protocol.ProtocolVersion, ID: "fixture.inbound.1", Payload: protocol.InboundMessage{SourceID: "source.message.1", Route: defaultSurface.Route, ConversationID: defaultSurface.ConversationID, SurfaceKind: defaultSurface.Kind, SurfaceKey: defaultSurface.SurfaceKey, PrincipalKey: defaultSurface.PrincipalKey, Message: protocol.MessageRef{Handle: "message_1"}, Author: protocol.Author{Handle: "author_1", Label: "Operator"}, Text: "hello"}}
		if err := encoder.Write(inboundEnvelope, protocol.FromAdapter); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		frame, readErr := decoder.Read(protocol.FromHost)
		if readErr != nil {
			return readErr
		}
		switch payload := frame.Payload.(type) {
		case *protocol.EventAck:
			if scripted && frame.CorrelationID == inboundEnvelope.ID && !replayed {
				if err := write(protocol.Connection{State: protocol.ConnectionReconnecting, Attempt: 1}, ""); err != nil {
					return err
				}
				if err := write(protocol.Connection{State: protocol.ConnectionReady, Attempt: 1}, ""); err != nil {
					return err
				}
				if err := encoder.Write(inboundEnvelope, protocol.FromAdapter); err != nil {
					return err
				}
				replayed = true
			} else if scripted && frame.CorrelationID == inboundEnvelope.ID && replayed && controlStage == 0 {
				status := protocol.Envelope{ProtocolVersion: protocol.ProtocolVersion, ID: "fixture.control.status.1", Payload: protocol.ControlRequest{SourceID: "source.status.1", Route: defaultSurface.Route, ConversationID: defaultSurface.ConversationID, SurfaceKind: defaultSurface.Kind, SurfaceKey: defaultSurface.SurfaceKey, PrincipalKey: defaultSurface.PrincipalKey, Message: protocol.MessageRef{Handle: "message_1"}, Action: protocol.ControlStatus}}
				if err := encoder.Write(status, protocol.FromAdapter); err != nil {
					return err
				}
				controlStage = 1
			}
		case *protocol.ControlResult:
			if !scripted || controlStage == 0 {
				return errors.New("fixture received unexpected control result")
			}
			if controlStage == 1 {
				if frame.CorrelationID != "fixture.control.status.1" || payload.Action != protocol.ControlStatus || payload.Disposition != protocol.ControlExact {
					return errors.New("fixture received invalid status result")
				}
				reset := protocol.Envelope{ProtocolVersion: protocol.ProtocolVersion, ID: "fixture.control.reset.1", Payload: protocol.ControlRequest{SourceID: "source.reset.1", Route: defaultSurface.Route, ConversationID: defaultSurface.ConversationID, SurfaceKind: defaultSurface.Kind, SurfaceKey: defaultSurface.SurfaceKey, PrincipalKey: defaultSurface.PrincipalKey, Message: protocol.MessageRef{Handle: "message_1"}, Action: protocol.ControlReset}}
				if err := encoder.Write(reset, protocol.FromAdapter); err != nil {
					return err
				}
				controlStage = 2
			} else if frame.CorrelationID != "fixture.control.reset.1" || payload.Action != protocol.ControlReset {
				return errors.New("fixture received invalid reset result")
			}
		case *protocol.Activity:
			// Activity is intentionally one-way and has no acknowledgement.
		case *protocol.Delivery:
			*deliveryCount = *deliveryCount + 1
			disposition := protocol.EffectExact
			var message *protocol.MessageRef
			if *deliveryCount == 1 {
				message = &protocol.MessageRef{Handle: "delivered_1"}
			} else {
				disposition = protocol.EffectAmbiguous
			}
			if err := write(protocol.DeliveryResult{Disposition: disposition, Message: message}, frame.ID); err != nil {
				return err
			}
		case *protocol.InteractionRequest:
			if err := write(protocol.InteractionReceipt{InteractionID: payload.InteractionID, Disposition: protocol.EffectExact}, frame.ID); err != nil {
				return err
			}
			if payload.Restore {
				continue
			}
			answer := protocol.SemanticInteractionAnswer{SchemaVersion: 1, Action: protocol.AnswerSubmit, Fields: []protocol.FieldAnswer{{FieldID: "confirmation", Confirmed: boolPointer(true)}}}
			if err := write(protocol.InteractionResult{InteractionID: payload.InteractionID, Answer: answer}, frame.ID); err != nil {
				return err
			}
		case *protocol.InteractionCancel:
			// Host-originated cancellation retires the external UI without
			// synthesizing a user answer.
		case *protocol.AttachmentFetch:
			if err := write(protocol.AttachmentChunk{TransferID: payload.TransferID, Sequence: 0, Data: "Zml4dHVyZQ==", Final: true}, frame.ID); err != nil {
				return err
			}
		case *protocol.AttachmentDeliver:
			if payload.Final {
				if err := write(protocol.AttachmentResult{TransferID: payload.TransferID, Disposition: protocol.EffectExact}, frame.ID); err != nil {
					return err
				}
			}
		case *protocol.Shutdown:
			return write(protocol.ShutdownComplete{}, frame.ID)
		default:
			return fmt.Errorf("fixture does not accept %T", payload)
		}
	}
}

// payloadKind is local bookkeeping only; the protocol encoder remains the
// authority for the closed wire kind.
func payloadKind(payload protocol.Payload) string { return fmt.Sprintf("%T", payload) }
func boolPointer(value bool) *bool                { return &value }
