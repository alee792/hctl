// Package channeladapter defines the dependency-free, versioned wire contract
// between hctl and an external channel adapter process. It is a semantic stdio
// protocol, not a vendor SDK, plugin runtime, or transport implementation.
package channeladapter

import "time"

const (
	ProtocolVersion = 1

	MaxFrameBytes           = 256 << 10
	MaxOperationResultBytes = 16 << 10
	MaxTextBytes            = 64 << 10
	MaxDiagnosticBytes      = 4 << 10
	MaxHandleBytes          = 256
	MaxIDBytes              = 128
	MaxProfileIDBytes       = 64
	MaxAttachments          = 16
	MaxAttachmentBytes      = 16 << 20
	MaxAttachmentChunkBytes = 64 << 10
	MaxQueuedFrames         = 64
	MaxQueuedBytes          = 8 << 20
	MaxOutstanding          = 128
	MaxTransfers            = 4
	MaxStderrBytes          = 64 << 10

	HandshakeTimeout  = 5 * time.Second
	CommandTimeout    = 30 * time.Second
	DeliveryTimeout   = 30 * time.Second
	AttachmentTimeout = 60 * time.Second
	ShutdownTimeout   = 5 * time.Second
	ForcedExitTimeout = 2 * time.Second
)

type Direction string

const (
	FromHost    Direction = "host"
	FromAdapter Direction = "adapter"
)

type Kind string

const (
	KindHello              Kind = "hello"
	KindInitialize         Kind = "initialize"
	KindReady              Kind = "ready"
	KindInboundMessage     Kind = "inbound_message"
	KindControlRequest     Kind = "control_request"
	KindControlResult      Kind = "control_result"
	KindEventAck           Kind = "event_ack"
	KindActivity           Kind = "activity"
	KindDelivery           Kind = "delivery"
	KindDeliveryResult     Kind = "delivery_result"
	KindInteractionRequest Kind = "interaction_request"
	KindInteractionReceipt Kind = "interaction_receipt"
	KindInteractionCancel  Kind = "interaction_cancel"
	KindInteractionResult  Kind = "interaction_result"
	KindAttachmentFetch    Kind = "attachment_fetch"
	KindAttachmentChunk    Kind = "attachment_chunk"
	KindAttachmentDeliver  Kind = "attachment_deliver"
	KindAttachmentResult   Kind = "attachment_result"
	KindConnection         Kind = "connection"
	KindDiagnostic         Kind = "diagnostic"
	KindShutdown           Kind = "shutdown"
	KindShutdownComplete   Kind = "shutdown_complete"
)

// Payload is a closed union. Only this module can add wire message kinds.
type Payload interface{ frameKind() Kind }

// Envelope carries one bounded semantic payload. ID is stable across an exact
// replay. CorrelationID names the frame whose command or event is being
// answered; it is never a vendor callback id.
type Envelope struct {
	ProtocolVersion int
	ID              string
	CorrelationID   string
	Payload         Payload
}

type Feature string

const (
	FeatureTyping                Feature = "typing"
	FeatureReplies               Feature = "replies"
	FeatureEdits                 Feature = "edits"
	FeatureReactions             Feature = "reactions"
	FeatureAttachments           Feature = "attachments"
	FeatureInteractiveComponents Feature = "interactive-components"
	FeatureTextFallback          Feature = "text-fallback"
)

type ProtocolRange struct {
	Minimum int `json:"minimum"`
	Before  int `json:"before"`
}

type Limits struct {
	MaxFrameBytes      int `json:"max_frame_bytes"`
	MaxTextBytes       int `json:"max_text_bytes"`
	MaxAttachments     int `json:"max_attachments"`
	MaxAttachmentBytes int `json:"max_attachment_bytes"`
	MaxOutstanding     int `json:"max_outstanding"`
}

type Hello struct {
	ChannelKind string        `json:"channel_kind"`
	Protocol    ProtocolRange `json:"protocol"`
	Features    []Feature     `json:"features"`
	Limits      Limits        `json:"limits"`
}

func (Hello) frameKind() Kind { return KindHello }

type ParticipationMode string

const ParticipationAmbient ParticipationMode = "ambient"

// RuntimePolicy contains enforced transport-neutral ceilings only. Authored
// policy prose, credentials, environment, execution policy, and paths do not
// cross into the adapter.
type RuntimePolicy struct {
	Participation        ParticipationMode `json:"participation"`
	MaxInboundTextBytes  int               `json:"max_inbound_text_bytes"`
	MaxDeliveryTextBytes int               `json:"max_delivery_text_bytes"`
	MaxAttachmentBytes   int               `json:"max_attachment_bytes"`
}

type Initialize struct {
	SelectedVersion int           `json:"selected_version"`
	ProfileID       string        `json:"profile_id"`
	Features        []Feature     `json:"features"`
	Limits          Limits        `json:"limits"`
	Policy          RuntimePolicy `json:"policy"`
}

func (Initialize) frameKind() Kind { return KindInitialize }

type Ready struct {
	ChannelKind string    `json:"channel_kind"`
	Features    []Feature `json:"features"`
	Limits      Limits    `json:"limits"`
	Surfaces    []Surface `json:"surfaces,omitempty"`
}

func (Ready) frameKind() Kind { return KindReady }

type Route struct {
	Handle string `json:"handle"`
}
type MessageRef struct {
	Handle string `json:"handle"`
}
type Author struct {
	Handle string `json:"handle"`
	Label  string `json:"label,omitempty"`
}

type SurfaceKind string

const (
	SurfaceDirect SurfaceKind = "direct"
	SurfaceShared SurfaceKind = "shared"
)

// Surface is an authorized transport-neutral conversation identity. Route is
// adapter-opaque; ConversationID and owner keys are stable semantic identities
// used only by hctl's durable controller.
type Surface struct {
	Route          Route       `json:"route"`
	ConversationID string      `json:"conversation_id"`
	Kind           SurfaceKind `json:"kind"`
	SurfaceKey     string      `json:"surface_key"`
	PrincipalKey   string      `json:"principal_key"`
}

type AttachmentDescriptor struct {
	Handle    string `json:"handle"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size"`
}

type InboundMessage struct {
	SourceID       string                 `json:"source_id"`
	Route          Route                  `json:"route"`
	ConversationID string                 `json:"conversation_id"`
	SurfaceKind    SurfaceKind            `json:"surface_kind"`
	SurfaceKey     string                 `json:"surface_key"`
	PrincipalKey   string                 `json:"principal_key"`
	Message        MessageRef             `json:"message"`
	Author         Author                 `json:"author"`
	Text           string                 `json:"text"`
	Attachments    []AttachmentDescriptor `json:"attachments,omitempty"`
}

func (InboundMessage) frameKind() Kind { return KindInboundMessage }

type ControlAction string

const (
	ControlStatus ControlAction = "status"
	ControlReset  ControlAction = "reset"
)

// ControlRequest is emitted only after the adapter authorizes and decodes its
// vendor-native status/reset command. Hctl retains lifecycle ownership.
type ControlRequest struct {
	SourceID       string        `json:"source_id"`
	Route          Route         `json:"route"`
	ConversationID string        `json:"conversation_id"`
	SurfaceKind    SurfaceKind   `json:"surface_kind"`
	SurfaceKey     string        `json:"surface_key"`
	PrincipalKey   string        `json:"principal_key"`
	Message        MessageRef    `json:"message"`
	Action         ControlAction `json:"action"`
}

func (ControlRequest) frameKind() Kind { return KindControlRequest }

type LifecycleState string

const (
	LifecycleInactive   LifecycleState = "inactive"
	LifecycleIdle       LifecycleState = "idle"
	LifecycleQueued     LifecycleState = "queued"
	LifecycleActive     LifecycleState = "active"
	LifecycleWaiting    LifecycleState = "waiting_for_input"
	LifecycleHibernated LifecycleState = "hibernated"
)

type RuntimeStatus struct {
	Agent         string         `json:"agent"`
	Harness       string         `json:"harness"`
	State         LifecycleState `json:"state"`
	Pending       int            `json:"pending"`
	Active        int            `json:"active"`
	ActiveLimit   int            `json:"active_limit"`
	Resident      int            `json:"resident"`
	ResidentLimit int            `json:"resident_limit"`
	Queued        int            `json:"queued"`
}
type ControlDisposition string

const (
	ControlExact  ControlDisposition = "exact"
	ControlBusy   ControlDisposition = "busy"
	ControlFailed ControlDisposition = "failed"
)

type ControlResult struct {
	Action      ControlAction      `json:"action"`
	Disposition ControlDisposition `json:"disposition"`
	Status      *RuntimeStatus     `json:"status,omitempty"`
	Failure     Failure            `json:"failure,omitempty"`
}

func (ControlResult) frameKind() Kind { return KindControlResult }

type EventAck struct {
	Disposition string `json:"disposition"`
}

func (EventAck) frameKind() Kind { return KindEventAck }

type ActivityKind string

const (
	ActivityTyping ActivityKind = "typing"
	ActivityActive ActivityKind = "active"
	ActivityIdle   ActivityKind = "idle"
)

type Activity struct {
	Route Route        `json:"route"`
	Kind  ActivityKind `json:"kind"`
}

func (Activity) frameKind() Kind { return KindActivity }

type DeliveryAction string

const (
	DeliverySend     DeliveryAction = "send"
	DeliveryEdit     DeliveryAction = "edit"
	DeliveryReaction DeliveryAction = "reaction"
)

type Delivery struct {
	Action              DeliveryAction `json:"action"`
	Route               Route          `json:"route"`
	Message             *MessageRef    `json:"message,omitempty"`
	ReplyTo             *MessageRef    `json:"reply_to,omitempty"`
	Text                string         `json:"text,omitempty"`
	Reaction            string         `json:"reaction,omitempty"`
	AttachmentTransfers []string       `json:"attachment_transfers,omitempty"`
}

func (Delivery) frameKind() Kind { return KindDelivery }

type EffectDisposition string

const (
	EffectExact     EffectDisposition = "exact"
	EffectAmbiguous EffectDisposition = "ambiguous"
	EffectFailed    EffectDisposition = "failed"
)

type DeliveryResult struct {
	Disposition EffectDisposition `json:"disposition"`
	Message     *MessageRef       `json:"message,omitempty"`
	Failure     Failure           `json:"failure,omitempty"`
}

func (DeliveryResult) frameKind() Kind { return KindDeliveryResult }

type InteractionKind string

const (
	InteractionConfirm    InteractionKind = "confirm"
	InteractionChooseOne  InteractionKind = "choose_one"
	InteractionChooseMany InteractionKind = "choose_many"
	InteractionText       InteractionKind = "text"
	InteractionDateTime   InteractionKind = "date_time"
	InteractionForm       InteractionKind = "form"
)

type Cancellation string

const (
	CancellationAllowed   Cancellation = "allowed"
	CancellationForbidden Cancellation = "forbidden"
)

type DateTimeRepresentation string

const (
	DateOnly DateTimeRepresentation = "date"
	TimeOnly DateTimeRepresentation = "time"
	DateTime DateTimeRepresentation = "date_time"
)

type InteractionPolicy struct {
	ExpiresAfterSeconds int          `json:"expires_after_seconds"`
	Cancellation        Cancellation `json:"cancellation"`
}
type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value"`
}
type Field struct {
	ID                     string                 `json:"id"`
	Kind                   InteractionKind        `json:"kind"`
	Label                  string                 `json:"label"`
	Description            string                 `json:"description,omitempty"`
	Required               bool                   `json:"required"`
	Options                []Option               `json:"options,omitempty"`
	AllowFreeform          bool                   `json:"allow_freeform,omitempty"`
	MinSelections          int                    `json:"min_selections,omitempty"`
	MaxSelections          int                    `json:"max_selections,omitempty"`
	MinLength              int                    `json:"min_length,omitempty"`
	MaxLength              int                    `json:"max_length,omitempty"`
	DateTimeRepresentation DateTimeRepresentation `json:"date_time_representation,omitempty"`
}
type SemanticInteractionRequest struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          InteractionKind   `json:"kind"`
	Prompt        string            `json:"prompt"`
	FallbackText  string            `json:"fallback_text,omitempty"`
	Policy        InteractionPolicy `json:"policy"`
	Field         *Field            `json:"field,omitempty"`
	Fields        []Field           `json:"fields,omitempty"`
}
type InteractionRequest struct {
	InteractionID string                     `json:"interaction_id"`
	Route         Route                      `json:"route"`
	ReplyTo       MessageRef                 `json:"reply_to"`
	Restore       bool                       `json:"restore,omitempty"`
	Request       SemanticInteractionRequest `json:"request"`
}

func (InteractionRequest) frameKind() Kind { return KindInteractionRequest }

type InteractionReceipt struct {
	InteractionID string            `json:"interaction_id"`
	Disposition   EffectDisposition `json:"disposition"`
	Failure       Failure           `json:"failure,omitempty"`
}

func (InteractionReceipt) frameKind() Kind { return KindInteractionReceipt }

type InteractionCancel struct {
	InteractionID string `json:"interaction_id"`
}

func (InteractionCancel) frameKind() Kind { return KindInteractionCancel }

type AnswerAction string

const (
	AnswerSubmit AnswerAction = "submit"
	AnswerCancel AnswerAction = "cancel"
)

type FieldAnswer struct {
	FieldID   string   `json:"field_id"`
	Confirmed *bool    `json:"confirmed,omitempty"`
	OptionIDs []string `json:"option_ids,omitempty"`
	Freeform  *string  `json:"freeform,omitempty"`
	Text      *string  `json:"text,omitempty"`
	DateTime  *string  `json:"date_time,omitempty"`
}
type SemanticInteractionAnswer struct {
	SchemaVersion int           `json:"schema_version"`
	Action        AnswerAction  `json:"action"`
	Fields        []FieldAnswer `json:"fields,omitempty"`
}
type InteractionResult struct {
	InteractionID string                    `json:"interaction_id"`
	Answer        SemanticInteractionAnswer `json:"answer"`
}

func (InteractionResult) frameKind() Kind { return KindInteractionResult }

type AttachmentFetch struct {
	TransferID       string `json:"transfer_id"`
	AttachmentHandle string `json:"attachment_handle"`
	MaximumBytes     int    `json:"maximum_bytes"`
}

func (AttachmentFetch) frameKind() Kind { return KindAttachmentFetch }

type AttachmentChunk struct {
	TransferID string `json:"transfer_id"`
	Sequence   int    `json:"sequence"`
	Data       string `json:"data"`
	Final      bool   `json:"final"`
}

func (AttachmentChunk) frameKind() Kind { return KindAttachmentChunk }

type AttachmentDeliver struct {
	TransferID string `json:"transfer_id"`
	Sequence   int    `json:"sequence"`
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	Data       string `json:"data"`
	Final      bool   `json:"final"`
}

func (AttachmentDeliver) frameKind() Kind { return KindAttachmentDeliver }

type AttachmentResult struct {
	TransferID  string            `json:"transfer_id"`
	Disposition EffectDisposition `json:"disposition"`
	Failure     Failure           `json:"failure,omitempty"`
}

func (AttachmentResult) frameKind() Kind { return KindAttachmentResult }

type ConnectionState string

const (
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionReady        ConnectionState = "ready"
	ConnectionReconnecting ConnectionState = "reconnecting"
	ConnectionDegraded     ConnectionState = "degraded"
	ConnectionClosed       ConnectionState = "closed"
)

type Connection struct {
	State   ConnectionState `json:"state"`
	Attempt int             `json:"attempt"`
}

func (Connection) frameKind() Kind { return KindConnection }

type DiagnosticClass string

const (
	DiagnosticConfiguration  DiagnosticClass = "configuration"
	DiagnosticAuthentication DiagnosticClass = "authentication"
	DiagnosticConnection     DiagnosticClass = "connection"
	DiagnosticRateLimit      DiagnosticClass = "rate_limit"
	DiagnosticProtocol       DiagnosticClass = "protocol"
	DiagnosticInternal       DiagnosticClass = "internal"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Diagnostic struct {
	Class    DiagnosticClass `json:"class"`
	Severity Severity        `json:"severity"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

func (Diagnostic) frameKind() Kind { return KindDiagnostic }

type Failure struct {
	Class DiagnosticClass `json:"class"`
	Code  string          `json:"code"`
}

type Shutdown struct {
	Reason string `json:"reason"`
}

func (Shutdown) frameKind() Kind { return KindShutdown }

type ShutdownComplete struct{}

func (ShutdownComplete) frameKind() Kind { return KindShutdownComplete }

// OperationResult is the only machine-readable stdout value from setup,
// status, or remove mode. Setup reads secrets from its inherited trusted
// terminal; no secret or secret reference is represented here.
type OperationResult struct {
	SchemaVersion int    `json:"schema_version"`
	Operation     string `json:"operation"`
	ProfileID     string `json:"profile_id"`
	Status        string `json:"status"`
	Identity      string `json:"identity,omitempty"`
	Message       string `json:"message,omitempty"`
}
