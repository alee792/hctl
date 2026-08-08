package channeladapter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	frameIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	handlePattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	semanticIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	channelKindPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	profileIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	codePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

func ValidateEnvelope(envelope Envelope, direction Direction) error {
	if envelope.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("channel-adapter protocol version %d is unsupported", envelope.ProtocolVersion)
	}
	if !frameIDPattern.MatchString(envelope.ID) {
		return errors.New("channel-adapter frame id is invalid")
	}
	if payloadMissing(envelope.Payload) {
		return errors.New("channel-adapter payload is missing")
	}
	kind := envelope.Payload.frameKind()
	if !directionAllows(direction, kind) {
		return fmt.Errorf("channel-adapter %s frame cannot travel from %s", kind, direction)
	}
	requiresCorrelation := kind == KindInitialize || kind == KindReady || kind == KindControlResult || kind == KindEventAck || kind == KindDeliveryResult || kind == KindInteractionResult || kind == KindAttachmentChunk || kind == KindAttachmentResult || kind == KindShutdownComplete
	allowsCorrelation := requiresCorrelation || kind == KindDiagnostic
	if requiresCorrelation && !frameIDPattern.MatchString(envelope.CorrelationID) {
		return fmt.Errorf("channel-adapter %s frame requires correlation", kind)
	}
	if !allowsCorrelation && envelope.CorrelationID != "" {
		return fmt.Errorf("channel-adapter %s frame must not contain correlation", kind)
	}
	if envelope.CorrelationID != "" && !frameIDPattern.MatchString(envelope.CorrelationID) {
		return errors.New("channel-adapter correlation id is invalid")
	}
	if envelope.CorrelationID == envelope.ID {
		return errors.New("channel-adapter frame cannot correlate to itself")
	}
	return validatePayload(envelope.Payload)
}

func payloadMissing(payload Payload) bool {
	if payload == nil {
		return true
	}
	value := reflect.ValueOf(payload)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func directionAllows(direction Direction, kind Kind) bool {
	switch direction {
	case FromHost:
		switch kind {
		case KindInitialize, KindControlResult, KindEventAck, KindActivity, KindDelivery, KindInteractionRequest, KindInteractionCancel, KindAttachmentFetch, KindAttachmentDeliver, KindShutdown:
			return true
		}
	case FromAdapter:
		switch kind {
		case KindHello, KindReady, KindInboundMessage, KindControlRequest, KindDeliveryResult, KindInteractionResult, KindAttachmentChunk, KindAttachmentResult, KindConnection, KindDiagnostic, KindShutdownComplete:
			return true
		}
	}
	return false
}

func validatePayload(payload Payload) error {
	switch value := payload.(type) {
	case Hello:
		return validateHello(value)
	case *Hello:
		return validateHello(*value)
	case Initialize:
		return validateInitialize(value)
	case *Initialize:
		return validateInitialize(*value)
	case Ready:
		return validateReady(value)
	case *Ready:
		return validateReady(*value)
	case InboundMessage:
		return validateInbound(value)
	case *InboundMessage:
		return validateInbound(*value)
	case ControlRequest:
		return validateControlRequest(value)
	case *ControlRequest:
		return validateControlRequest(*value)
	case ControlResult:
		return validateControlResult(value)
	case *ControlResult:
		return validateControlResult(*value)
	case EventAck:
		return validateEnum("event acknowledgement", value.Disposition, "accepted", "duplicate", "rejected")
	case *EventAck:
		return validateEnum("event acknowledgement", value.Disposition, "accepted", "duplicate", "rejected")
	case Activity:
		return validateActivity(value)
	case *Activity:
		return validateActivity(*value)
	case Delivery:
		return validateDelivery(value)
	case *Delivery:
		return validateDelivery(*value)
	case DeliveryResult:
		return validateDeliveryResult(value)
	case *DeliveryResult:
		return validateDeliveryResult(*value)
	case InteractionRequest:
		return validateInteractionRequest(value)
	case *InteractionRequest:
		return validateInteractionRequest(*value)
	case InteractionCancel:
		return validateRuntimeID(value.InteractionID)
	case *InteractionCancel:
		return validateRuntimeID(value.InteractionID)
	case InteractionResult:
		return validateInteractionResult(value)
	case *InteractionResult:
		return validateInteractionResult(*value)
	case AttachmentFetch:
		return validateAttachmentFetch(value)
	case *AttachmentFetch:
		return validateAttachmentFetch(*value)
	case AttachmentChunk:
		return validateAttachmentChunk(value.TransferID, value.Sequence, value.Data)
	case *AttachmentChunk:
		return validateAttachmentChunk(value.TransferID, value.Sequence, value.Data)
	case AttachmentDeliver:
		if err := validateAttachmentChunk(value.TransferID, value.Sequence, value.Data); err != nil {
			return err
		}
		return validateAttachmentName(value.Name, value.MediaType, value.Sequence == 0)
	case *AttachmentDeliver:
		if err := validateAttachmentChunk(value.TransferID, value.Sequence, value.Data); err != nil {
			return err
		}
		return validateAttachmentName(value.Name, value.MediaType, value.Sequence == 0)
	case AttachmentResult:
		return validateAttachmentResult(value)
	case *AttachmentResult:
		return validateAttachmentResult(*value)
	case Connection:
		return validateConnection(value)
	case *Connection:
		return validateConnection(*value)
	case Diagnostic:
		return validateDiagnostic(value)
	case *Diagnostic:
		return validateDiagnostic(*value)
	case Shutdown:
		return validateText("shutdown reason", value.Reason, 0, 256)
	case *Shutdown:
		return validateText("shutdown reason", value.Reason, 0, 256)
	case ShutdownComplete, *ShutdownComplete:
		return nil
	default:
		return errors.New("channel-adapter payload type is unsupported")
	}
}

func validateHello(value Hello) error {
	if !channelKindPattern.MatchString(value.ChannelKind) || len(value.ChannelKind) > 64 {
		return errors.New("channel kind is invalid")
	}
	if value.Protocol.Minimum < 1 || value.Protocol.Before <= value.Protocol.Minimum || value.Protocol.Minimum > ProtocolVersion || value.Protocol.Before <= ProtocolVersion {
		return errors.New("adapter protocol range does not include v1")
	}
	if err := validateFeatures(value.Features, true); err != nil {
		return err
	}
	return validateLimits(value.Limits)
}

func validateInitialize(value Initialize) error {
	if value.SelectedVersion != ProtocolVersion {
		return errors.New("selected protocol version is unsupported")
	}
	if !profileIDPattern.MatchString(value.ProfileID) || len(value.ProfileID) > MaxProfileIDBytes {
		return errors.New("profile id is invalid")
	}
	if err := validateFeatures(value.Features, false); err != nil {
		return err
	}
	if err := validateLimits(value.Limits); err != nil {
		return err
	}
	if value.Policy.Participation != ParticipationAmbient {
		return errors.New("runtime participation policy must be ambient")
	}
	if value.Policy.MaxInboundTextBytes < 1 || value.Policy.MaxInboundTextBytes > MaxTextBytes || value.Policy.MaxDeliveryTextBytes < 1 || value.Policy.MaxDeliveryTextBytes > MaxTextBytes || value.Policy.MaxAttachmentBytes < 1 || value.Policy.MaxAttachmentBytes > MaxAttachmentBytes {
		return errors.New("runtime policy limit is invalid")
	}
	return nil
}

func validateReady(value Ready) error {
	if !channelKindPattern.MatchString(value.ChannelKind) || len(value.ChannelKind) > 64 {
		return errors.New("channel kind is invalid")
	}
	if err := validateFeatures(value.Features, false); err != nil {
		return err
	}
	return validateLimits(value.Limits)
}

func validateFeatures(features []Feature, require bool) error {
	if require && len(features) == 0 || len(features) > 7 {
		return errors.New("feature declaration is invalid")
	}
	allowed := map[Feature]bool{FeatureTyping: true, FeatureReplies: true, FeatureEdits: true, FeatureReactions: true, FeatureAttachments: true, FeatureInteractiveComponents: true, FeatureTextFallback: true}
	seen := map[Feature]bool{}
	for _, feature := range features {
		if !allowed[feature] || seen[feature] {
			return fmt.Errorf("feature %q is unsupported or duplicated", feature)
		}
		seen[feature] = true
	}
	return nil
}

func validateLimits(limits Limits) error {
	if limits.MaxFrameBytes < 1 || limits.MaxFrameBytes > MaxFrameBytes || limits.MaxTextBytes < 1 || limits.MaxTextBytes > MaxTextBytes || limits.MaxAttachments < 0 || limits.MaxAttachments > MaxAttachments || limits.MaxAttachmentBytes < 0 || limits.MaxAttachmentBytes > MaxAttachmentBytes || limits.MaxOutstanding < 1 || limits.MaxOutstanding > MaxOutstanding {
		return errors.New("channel-adapter negotiated limits are invalid")
	}
	return nil
}

// ValidateNegotiation proves that runtime negotiation only narrows both the
// adapter hello and the host initialization. Manifest feature narrowing is
// checked by the package consumer before process launch.
func ValidateNegotiation(hello Hello, initialize Initialize, ready Ready) error {
	if err := validateHello(hello); err != nil {
		return err
	}
	if err := validateInitialize(initialize); err != nil {
		return err
	}
	if err := validateReady(ready); err != nil {
		return err
	}
	if hello.ChannelKind != ready.ChannelKind {
		return errors.New("ready channel kind changed after hello")
	}
	if !featureSubset(initialize.Features, hello.Features) || !featureSubset(ready.Features, initialize.Features) {
		return errors.New("ready features did not narrow the declared handshake")
	}
	if !limitsNarrow(initialize.Limits, hello.Limits) || !limitsNarrow(ready.Limits, initialize.Limits) {
		return errors.New("ready limits did not narrow the declared handshake")
	}
	return nil
}

func featureSubset(candidate, allowed []Feature) bool {
	set := map[Feature]bool{}
	for _, feature := range allowed {
		set[feature] = true
	}
	for _, feature := range candidate {
		if !set[feature] {
			return false
		}
	}
	return true
}
func limitsNarrow(candidate, allowed Limits) bool {
	return candidate.MaxFrameBytes <= allowed.MaxFrameBytes && candidate.MaxTextBytes <= allowed.MaxTextBytes && candidate.MaxAttachments <= allowed.MaxAttachments && candidate.MaxAttachmentBytes <= allowed.MaxAttachmentBytes && candidate.MaxOutstanding <= allowed.MaxOutstanding
}

func validateInbound(value InboundMessage) error {
	if !frameIDPattern.MatchString(value.SourceID) {
		return errors.New("inbound source id is invalid")
	}
	if err := validateHandle(value.Route.Handle); err != nil {
		return err
	}
	if err := validateHandle(value.Message.Handle); err != nil {
		return err
	}
	if err := validateHandle(value.Author.Handle); err != nil {
		return err
	}
	if err := validateText("author label", value.Author.Label, 0, 256); err != nil {
		return err
	}
	if err := validateText("inbound text", value.Text, 0, MaxTextBytes); err != nil {
		return err
	}
	if len(value.Attachments) > MaxAttachments || value.Text == "" && len(value.Attachments) == 0 {
		return errors.New("inbound message content is empty or exceeds attachment limit")
	}
	seen := map[string]bool{}
	for _, attachment := range value.Attachments {
		if err := validateHandle(attachment.Handle); err != nil || seen[attachment.Handle] {
			return errors.New("attachment handle is invalid or duplicated")
		}
		seen[attachment.Handle] = true
		if err := validateAttachmentName(attachment.Name, attachment.MediaType, true); err != nil {
			return err
		}
		if attachment.Size < 0 || attachment.Size > MaxAttachmentBytes {
			return errors.New("attachment size is invalid")
		}
	}
	return nil
}

func validateControlRequest(value ControlRequest) error {
	if !frameIDPattern.MatchString(value.SourceID) {
		return errors.New("control source id is invalid")
	}
	if err := validateHandle(value.Route.Handle); err != nil {
		return err
	}
	if err := validateHandle(value.Message.Handle); err != nil {
		return err
	}
	if value.Action != ControlStatus && value.Action != ControlReset {
		return errors.New("control action is invalid")
	}
	return nil
}

func validateControlResult(value ControlResult) error {
	if value.Action != ControlStatus && value.Action != ControlReset {
		return errors.New("control action is invalid")
	}
	if value.Disposition != ControlExact && value.Disposition != ControlBusy && value.Disposition != ControlFailed {
		return errors.New("control disposition is invalid")
	}
	if value.Action == ControlStatus && value.Disposition == ControlExact {
		if value.Status == nil {
			return errors.New("exact status control result is missing status")
		}
		if err := validateRuntimeStatus(*value.Status); err != nil {
			return err
		}
	} else if value.Status != nil {
		return errors.New("control result contains inapplicable status")
	}
	if value.Disposition == ControlFailed {
		return validateFailure(value.Failure)
	}
	if value.Failure != (Failure{}) {
		return errors.New("control result contains inapplicable failure")
	}
	return nil
}

func validateRuntimeStatus(value RuntimeStatus) error {
	if err := validateText("status agent", value.Agent, 1, 128); err != nil {
		return err
	}
	if value.Harness != "claude" && value.Harness != "codex" {
		return errors.New("status harness is invalid")
	}
	if value.State != LifecycleInactive && value.State != LifecycleIdle && value.State != LifecycleQueued && value.State != LifecycleActive && value.State != LifecycleWaiting && value.State != LifecycleHibernated {
		return errors.New("status lifecycle is invalid")
	}
	if value.Pending < 0 || value.Pending > 1<<20 || value.Active < 0 || value.ActiveLimit < 1 || value.Active > value.ActiveLimit || value.ActiveLimit > 64 || value.Resident < 0 || value.ResidentLimit < 1 || value.Resident > value.ResidentLimit || value.ResidentLimit > 64 || value.ActiveLimit > value.ResidentLimit || value.Queued < 0 || value.Queued > 1<<20 {
		return errors.New("status capacity is invalid")
	}
	return nil
}

func validateActivity(value Activity) error {
	if err := validateHandle(value.Route.Handle); err != nil {
		return err
	}
	if value.Kind != ActivityTyping && value.Kind != ActivityActive && value.Kind != ActivityIdle {
		return errors.New("activity kind is invalid")
	}
	return nil
}

func validateDelivery(value Delivery) error {
	if err := validateHandle(value.Route.Handle); err != nil {
		return err
	}
	if value.Message != nil {
		if err := validateHandle(value.Message.Handle); err != nil {
			return err
		}
	}
	if value.ReplyTo != nil {
		if err := validateHandle(value.ReplyTo.Handle); err != nil {
			return err
		}
	}
	if err := validateText("delivery text", value.Text, 0, MaxTextBytes); err != nil {
		return err
	}
	if err := validateText("delivery reaction", value.Reaction, 0, 128); err != nil {
		return err
	}
	if len(value.AttachmentTransfers) > MaxAttachments {
		return errors.New("delivery exceeds attachment limit")
	}
	switch value.Action {
	case DeliverySend:
		if value.Message != nil || value.Reaction != "" || value.Text == "" && len(value.AttachmentTransfers) == 0 {
			return errors.New("send delivery shape is invalid")
		}
	case DeliveryEdit:
		if value.Message == nil || value.ReplyTo != nil || value.Reaction != "" || value.Text == "" {
			return errors.New("edit delivery shape is invalid")
		}
	case DeliveryReaction:
		if value.Message == nil || value.ReplyTo != nil || value.Text != "" || len(value.AttachmentTransfers) != 0 || value.Reaction == "" {
			return errors.New("reaction delivery shape is invalid")
		}
	default:
		return errors.New("delivery action is invalid")
	}
	seen := map[string]bool{}
	for _, id := range value.AttachmentTransfers {
		if validateRuntimeID(id) != nil || seen[id] {
			return errors.New("delivery attachment transfer is invalid or duplicated")
		}
		seen[id] = true
	}
	return nil
}

func validateDeliveryResult(value DeliveryResult) error {
	if value.Disposition != EffectExact && value.Disposition != EffectAmbiguous && value.Disposition != EffectFailed {
		return errors.New("delivery disposition is invalid")
	}
	if value.Disposition == EffectExact && value.Message != nil {
		if err := validateHandle(value.Message.Handle); err != nil {
			return err
		}
	}
	if value.Disposition == EffectFailed {
		return validateFailure(value.Failure)
	}
	if value.Failure != (Failure{}) {
		return errors.New("successful or ambiguous delivery contains failure details")
	}
	return nil
}

func validateInteractionRequest(value InteractionRequest) error {
	if err := validateRuntimeID(value.InteractionID); err != nil {
		return err
	}
	if err := validateHandle(value.Route.Handle); err != nil {
		return err
	}
	if err := validateHandle(value.ReplyTo.Handle); err != nil {
		return err
	}
	return validateSemanticRequest(value.Request)
}

func validateSemanticRequest(request SemanticInteractionRequest) error {
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > 32<<10 {
		return errors.New("interaction request exceeds 32768 bytes")
	}
	if request.SchemaVersion != 1 {
		return errors.New("interaction schema version is invalid")
	}
	if err := validateMultilineText("interaction prompt", request.Prompt, 1, 2<<10); err != nil {
		return err
	}
	if request.FallbackText != "" {
		if err := validateMultilineText("interaction fallback", request.FallbackText, 1, 4<<10); err != nil {
			return err
		}
	}
	if request.Policy.ExpiresAfterSeconds < 60 || request.Policy.ExpiresAfterSeconds > 7*24*60*60 || request.Policy.Cancellation != CancellationAllowed && request.Policy.Cancellation != CancellationForbidden {
		return errors.New("interaction policy is invalid")
	}
	validKind := func(kind InteractionKind) bool {
		return kind == InteractionConfirm || kind == InteractionChooseOne || kind == InteractionChooseMany || kind == InteractionText || kind == InteractionDateTime || kind == InteractionForm
	}
	if !validKind(request.Kind) {
		return errors.New("interaction kind is invalid")
	}
	if request.Kind == InteractionForm {
		if request.Field != nil || len(request.Fields) == 0 || len(request.Fields) > 8 {
			return errors.New("interaction form fields are invalid")
		}
	} else if request.Field == nil || len(request.Fields) != 0 || request.Field.Kind != request.Kind || !request.Field.Required {
		return errors.New("interaction field shape is invalid")
	}
	fields := request.Fields
	if request.Field != nil {
		fields = []Field{*request.Field}
	}
	seen := map[string]bool{}
	totalOptions := 0
	for _, field := range fields {
		if !semanticIDPattern.MatchString(field.ID) || seen[field.ID] || !validKind(field.Kind) || field.Kind == InteractionForm {
			return errors.New("interaction field is invalid or duplicated")
		}
		seen[field.ID] = true
		if err := validateContractText("interaction field label", field.Label, 1, 100); err != nil {
			return err
		}
		if field.Description != "" {
			if err := validateContractText("interaction field description", field.Description, 1, 300); err != nil {
				return err
			}
		}
		if err := validateField(field); err != nil {
			return err
		}
		totalOptions += len(field.Options)
	}
	if totalOptions > 64 {
		return errors.New("interaction total option count exceeds 64")
	}
	return nil
}

func validateField(field Field) error {
	switch field.Kind {
	case InteractionConfirm:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.MinLength != 0 || field.MaxLength != 0 || field.DateTimeRepresentation != "" {
			return errors.New("confirm field contains inapplicable values")
		}
	case InteractionChooseOne, InteractionChooseMany:
		if field.DateTimeRepresentation != "" {
			return errors.New("choice field contains date-time representation")
		}
		if len(field.Options) == 0 || len(field.Options) > 25 {
			return errors.New("choice options are invalid")
		}
		maximumSelections := len(field.Options)
		if field.AllowFreeform {
			maximumSelections++
		}
		if field.Kind == InteractionChooseOne && (field.MaxSelections != 1 || field.MinSelections < 0 || field.MinSelections > 1) || field.Kind == InteractionChooseMany && (field.MaxSelections < 1 || field.MaxSelections > maximumSelections || field.MinSelections < 0 || field.MinSelections > field.MaxSelections) {
			return errors.New("choice selection bounds are invalid")
		}
		if field.Required && field.MinSelections < 1 {
			return errors.New("required choice must require a selection")
		}
		seen := map[string]bool{}
		for _, option := range field.Options {
			if !semanticIDPattern.MatchString(option.ID) || seen[option.ID] {
				return errors.New("choice option id is invalid or duplicated")
			}
			seen[option.ID] = true
			if validateContractText("option label", option.Label, 1, 100) != nil || option.Description != "" && validateContractText("option description", option.Description, 1, 300) != nil || validateContractText("option value", option.Value, 1, 256) != nil {
				return errors.New("choice option text is invalid")
			}
		}
		if field.AllowFreeform {
			if field.MaxLength < 1 || field.MaxLength > 4000 || field.MinLength < 0 || field.MinLength > field.MaxLength {
				return errors.New("choice freeform bounds are invalid")
			}
		} else if field.MinLength != 0 || field.MaxLength != 0 {
			return errors.New("choice without freeform contains text bounds")
		}
	case InteractionText:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.DateTimeRepresentation != "" {
			return errors.New("text field contains inapplicable values")
		}
		if field.MaxLength < 1 || field.MaxLength > 4000 || field.MinLength < 0 || field.MinLength > field.MaxLength || field.Required && field.MinLength < 1 {
			return errors.New("text bounds are invalid")
		}
	case InteractionDateTime:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.MinLength != 0 || field.MaxLength != 0 {
			return errors.New("date-time field contains inapplicable values")
		}
		if field.DateTimeRepresentation != DateOnly && field.DateTimeRepresentation != TimeOnly && field.DateTimeRepresentation != DateTime {
			return errors.New("date-time representation is invalid")
		}
	default:
		return errors.New("interaction field kind is invalid")
	}
	return nil
}

func validateInteractionResult(value InteractionResult) error {
	if err := validateRuntimeID(value.InteractionID); err != nil {
		return err
	}
	answer := value.Answer
	encoded, err := json.Marshal(answer)
	if err != nil || len(encoded) > 16<<10 {
		return errors.New("interaction answer exceeds 16384 bytes")
	}
	if answer.SchemaVersion != 1 || answer.Action != AnswerSubmit && answer.Action != AnswerCancel {
		return errors.New("interaction answer header is invalid")
	}
	if answer.Action == AnswerCancel && len(answer.Fields) != 0 {
		return errors.New("cancelled interaction contains fields")
	}
	if len(answer.Fields) > 8 {
		return errors.New("interaction answer has too many fields")
	}
	seen := map[string]bool{}
	for _, field := range answer.Fields {
		if !semanticIDPattern.MatchString(field.FieldID) || seen[field.FieldID] {
			return errors.New("answer field id is invalid or duplicated")
		}
		seen[field.FieldID] = true
		count := 0
		if field.Confirmed != nil {
			count++
		}
		if len(field.OptionIDs) > 0 {
			count++
		}
		if field.Freeform != nil {
			count++
		}
		if field.Text != nil {
			count++
		}
		if field.DateTime != nil {
			count++
		}
		if count == 0 || count > 2 || len(field.OptionIDs) > 26 {
			return errors.New("answer field shape is invalid")
		}
		if count == 2 && (len(field.OptionIDs) == 0 || field.Freeform == nil) {
			return errors.New("only choice options and freeform may share one answer field")
		}
		seenOptions := map[string]bool{}
		for _, id := range field.OptionIDs {
			if !semanticIDPattern.MatchString(id) || seenOptions[id] {
				return errors.New("answer option id is invalid or duplicated")
			}
			seenOptions[id] = true
		}
		if field.Freeform != nil && !validAnswerText(*field.Freeform) {
			return errors.New("answer freeform text is invalid")
		}
		if field.Text != nil && !validAnswerText(*field.Text) {
			return errors.New("answer text is invalid")
		}
		if field.DateTime != nil && validateContractText("answer date-time", *field.DateTime, 1, 128) != nil {
			return errors.New("answer date-time is invalid")
		}
	}
	return nil
}

func validateAttachmentFetch(value AttachmentFetch) error {
	if err := validateRuntimeID(value.TransferID); err != nil {
		return err
	}
	if err := validateHandle(value.AttachmentHandle); err != nil {
		return err
	}
	if value.MaximumBytes < 1 || value.MaximumBytes > MaxAttachmentBytes {
		return errors.New("attachment fetch maximum is invalid")
	}
	return nil
}
func validateAttachmentChunk(id string, sequence int, data string) error {
	if err := validateRuntimeID(id); err != nil {
		return err
	}
	if sequence < 0 || sequence > 1<<20 {
		return errors.New("attachment sequence is invalid")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(decoded) > MaxAttachmentChunkBytes {
		return errors.New("attachment chunk is not bounded canonical base64")
	}
	return nil
}
func validateAttachmentResult(value AttachmentResult) error {
	if err := validateRuntimeID(value.TransferID); err != nil {
		return err
	}
	if value.Disposition != EffectExact && value.Disposition != EffectAmbiguous && value.Disposition != EffectFailed {
		return errors.New("attachment disposition is invalid")
	}
	if value.Disposition == EffectFailed {
		return validateFailure(value.Failure)
	}
	if value.Failure != (Failure{}) {
		return errors.New("attachment result contains inapplicable failure")
	}
	return nil
}
func validateAttachmentName(name, mediaType string, required bool) error {
	if required {
		if err := validateText("attachment name", name, 1, 256); err != nil {
			return err
		}
	} else if name != "" {
		return errors.New("attachment name is only valid in the first chunk")
	}
	if name != "" && (filepath.Base(name) != name || strings.ContainsAny(name, `/\\`)) {
		return errors.New("attachment name must not be a path")
	}
	if err := validateText("attachment media type", mediaType, 0, 128); err != nil {
		return err
	}
	return nil
}

func validateConnection(value Connection) error {
	if value.State != ConnectionConnecting && value.State != ConnectionReady && value.State != ConnectionReconnecting && value.State != ConnectionDegraded && value.State != ConnectionClosed || value.Attempt < 0 || value.Attempt > 1<<20 {
		return errors.New("connection lifecycle is invalid")
	}
	return nil
}
func validateDiagnostic(value Diagnostic) error {
	if !validDiagnosticClass(value.Class) || value.Severity != SeverityInfo && value.Severity != SeverityWarning && value.Severity != SeverityError || !codePattern.MatchString(value.Code) {
		return errors.New("diagnostic classification is invalid")
	}
	if err := validateText("diagnostic message", value.Message, 1, MaxDiagnosticBytes); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxDiagnosticBytes {
		return fmt.Errorf("diagnostic payload exceeds %d bytes", MaxDiagnosticBytes)
	}
	return nil
}
func validateFailure(value Failure) error {
	if !validDiagnosticClass(value.Class) || !codePattern.MatchString(value.Code) {
		return errors.New("failure classification is invalid")
	}
	return nil
}
func validDiagnosticClass(value DiagnosticClass) bool {
	return value == DiagnosticConfiguration || value == DiagnosticAuthentication || value == DiagnosticConnection || value == DiagnosticRateLimit || value == DiagnosticProtocol || value == DiagnosticInternal
}

func validateHandle(value string) error {
	if !handlePattern.MatchString(value) {
		return errors.New("opaque handle is invalid")
	}
	return nil
}
func validateRuntimeID(value string) error {
	if !frameIDPattern.MatchString(value) {
		return errors.New("runtime id is invalid")
	}
	return nil
}
func validateEnum(label, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("%s is invalid", label)
}
func validateText(label, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || len(value) < minimum || len(value) > maximum || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be bounded UTF-8 without NUL", label)
	}
	return nil
}

func validateContractText(label, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value || containsControl(value) {
		return fmt.Errorf("%s must be trimmed bounded UTF-8 without control characters", label)
	}
	return nil
}

func validateMultilineText(label, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value || containsUnsupportedMultilineControl(value) {
		return fmt.Errorf("%s must be trimmed bounded UTF-8 without unsupported control characters", label)
	}
	return nil
}

func validAnswerText(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 4000 && !containsUnsupportedMultilineControl(value)
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func containsUnsupportedMultilineControl(value string) bool {
	for _, character := range value {
		if character < 0x20 && character != '\n' && character != '\t' || character == 0x7f {
			return true
		}
	}
	return false
}

func ValidateOperationResult(result OperationResult) error {
	if result.SchemaVersion != 1 {
		return errors.New("operation result schema version is invalid")
	}
	if err := validateEnum("operation", result.Operation, "setup", "status", "remove"); err != nil {
		return err
	}
	if !profileIDPattern.MatchString(result.ProfileID) || len(result.ProfileID) > MaxProfileIDBytes {
		return errors.New("operation profile id is invalid")
	}
	if err := validateEnum("operation status", result.Status, "ready", "absent", "updated", "removed", "failed"); err != nil {
		return err
	}
	if err := validateText("operation identity", result.Identity, 0, 256); err != nil {
		return err
	}
	if err := validateText("operation message", result.Message, 0, 1024); err != nil {
		return err
	}
	return nil
}
