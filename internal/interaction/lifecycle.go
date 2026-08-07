package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRuntimeIDBytes       = 128
	MaxContinuationKeyBytes = 256
	MaxTerminalTombstones   = 64
)

var (
	runtimeIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	dispatchInputPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
	ownerKeyPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	digestPattern        = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Phase string

const (
	PhaseRequested Phase = "requested"
	PhaseRendered  Phase = "rendered"
	PhaseAnswered  Phase = "answered"
	PhaseResuming  Phase = "resuming"
	PhaseCompleted Phase = "completed"
	PhaseCancelled Phase = "cancelled"
	PhaseExpired   Phase = "expired"
)

type ContinuationMode string

const (
	ContinuationNativeDeferredTool ContinuationMode = "native_deferred_tool"
	ContinuationTurn               ContinuationMode = "continuation_turn"
)

// DeliveryState records the internal commit-before-side-effect boundary. An
// intended or uncertain delivery is never automatically repeated.
type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryIntended  DeliveryState = "intended"
	DeliveryDelivered DeliveryState = "delivered"
	DeliveryUncertain DeliveryState = "uncertain"
)

// ResumeState records the equivalent boundary for harness continuation.
type ResumeState string

const (
	ResumePending   ResumeState = "pending"
	ResumeIntended  ResumeState = "intended"
	ResumeFailed    ResumeState = "failed"
	ResumeUncertain ResumeState = "uncertain"
)

// Owner contains only keyed, pseudonymous ownership values. Vendor IDs and
// callback payloads remain process-local to the channel adapter.
type Owner struct {
	SurfaceKey   string `json:"surface_key"`
	PrincipalKey string `json:"principal_key"`
}

// Lifecycle is the one durable pending human-input request for a conversation.
// It contains semantic interaction data and runtime-owned correlation only.
type Lifecycle struct {
	ID              string           `json:"id"`
	InputID         string           `json:"input_id"`
	Owner           Owner            `json:"owner"`
	Request         Request          `json:"request"`
	Resolution      Resolution       `json:"resolution"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Continuation    ContinuationMode `json:"continuation"`
	ContinuationKey string           `json:"continuation_key,omitempty"`
	Phase           Phase            `json:"phase"`
	Delivery        DeliveryState    `json:"delivery"`
	Answer          *Answer          `json:"answer,omitempty"`
	AnswerDigest    string           `json:"answer_digest,omitempty"`
	Resume          ResumeState      `json:"resume,omitempty"`
}

// Tombstone retains only hashes needed to classify duplicate and late
// callbacks. Semantic content and raw runtime identifiers are deliberately
// discarded.
type Tombstone struct {
	InteractionDigest string    `json:"interaction_digest"`
	OwnerDigest       string    `json:"owner_digest"`
	AnswerDigest      string    `json:"answer_digest,omitempty"`
	Phase             Phase     `json:"phase"`
	FinishedAt        time.Time `json:"finished_at"`
}

type DurableState struct {
	Pending    *Lifecycle
	Tombstones []Tombstone
}

// FinishRequest describes one terminal interaction commit. The dispatch state
// owner uses it to complete the parked originating input in the same write.
type FinishRequest struct {
	InteractionID   string
	Phase           Phase
	AnswerDigest    string
	OriginOutcome   string
	ResultSessionID string
	FinishedAt      time.Time
	Recovery        bool
}

func (s DurableState) Validate() error {
	if len(s.Tombstones) > MaxTerminalTombstones {
		return errors.New("interaction tombstone limit exceeded")
	}
	if s.Pending != nil {
		if err := s.Pending.Validate(); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(s.Tombstones))
	for _, tombstone := range s.Tombstones {
		if err := tombstone.Validate(); err != nil || seen[tombstone.InteractionDigest] {
			return errors.New("interaction tombstone state is invalid")
		}
		seen[tombstone.InteractionDigest] = true
	}
	if s.Pending != nil && seen[Digest(s.Pending.ID)] {
		return errors.New("pending interaction conflicts with a tombstone")
	}
	return nil
}

func (l Lifecycle) Validate() error {
	if !runtimeIDPattern.MatchString(l.ID) || !dispatchInputPattern.MatchString(l.InputID) {
		return errors.New("interaction runtime correlation is invalid")
	}
	if err := l.Owner.Validate(); err != nil {
		return err
	}
	if err := ValidateRequest(l.Request); err != nil {
		return errors.New("interaction request is invalid")
	}
	if l.Resolution.Mode != RenderNative && l.Resolution.Mode != RenderTextFallback {
		return errors.New("interaction render resolution is invalid")
	}
	if l.Resolution.Mode == RenderTextFallback && l.Resolution.FallbackText == "" || l.Resolution.Mode == RenderNative && l.Resolution.FallbackText != "" {
		return errors.New("interaction render resolution is invalid")
	}
	if l.ExpiresAt.IsZero() || l.ExpiresAt.Location() != time.UTC || l.ExpiresAt.Nanosecond() != 0 {
		return errors.New("interaction expiry is invalid")
	}
	if l.Continuation != ContinuationNativeDeferredTool && l.Continuation != ContinuationTurn {
		return errors.New("interaction continuation mode is invalid")
	}
	if l.ContinuationKey != "" && (!utf8.ValidString(l.ContinuationKey) || len(l.ContinuationKey) > MaxContinuationKeyBytes || hasControl(l.ContinuationKey)) {
		return errors.New("interaction continuation key is invalid")
	}
	if l.Continuation == ContinuationNativeDeferredTool && l.ContinuationKey == "" {
		return errors.New("native deferred interaction continuation key is missing")
	}
	if l.Continuation == ContinuationTurn && l.ContinuationKey != "" {
		return errors.New("continuation turn contains a native continuation key")
	}
	if l.Phase != PhaseRequested && l.Phase != PhaseRendered && l.Phase != PhaseAnswered && l.Phase != PhaseResuming {
		return errors.New("pending interaction phase is invalid")
	}
	if l.Delivery != DeliveryPending && l.Delivery != DeliveryIntended && l.Delivery != DeliveryDelivered && l.Delivery != DeliveryUncertain {
		return errors.New("interaction delivery state is invalid")
	}
	switch l.Phase {
	case PhaseRequested:
		if l.Delivery == DeliveryDelivered || l.Answer != nil || l.AnswerDigest != "" || l.Resume != "" {
			return errors.New("requested interaction contains later state")
		}
	case PhaseRendered:
		if l.Delivery != DeliveryDelivered || l.Answer != nil || l.AnswerDigest != "" || l.Resume != "" {
			return errors.New("rendered interaction state is invalid")
		}
	case PhaseAnswered, PhaseResuming:
		if l.Delivery != DeliveryDelivered || l.Answer == nil || !digestPattern.MatchString(l.AnswerDigest) {
			return errors.New("answered interaction state is invalid")
		}
		if _, err := NormalizeAnswer(l.Request, *l.Answer); err != nil {
			return errors.New("interaction answer is invalid")
		}
		digest, err := DigestAnswer(*l.Answer)
		if err != nil || digest != l.AnswerDigest {
			return errors.New("interaction answer digest is invalid")
		}
		if l.Phase == PhaseAnswered && l.Resume != ResumePending || l.Phase == PhaseResuming && l.Resume != ResumeIntended && l.Resume != ResumeFailed && l.Resume != ResumeUncertain {
			return errors.New("interaction resume state is invalid")
		}
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func (o Owner) Validate() error {
	if !ownerKeyPattern.MatchString(o.SurfaceKey) || !ownerKeyPattern.MatchString(o.PrincipalKey) {
		return errors.New("interaction owner is invalid")
	}
	return nil
}

func (t Tombstone) Validate() error {
	if !digestPattern.MatchString(t.InteractionDigest) || !digestPattern.MatchString(t.OwnerDigest) || t.AnswerDigest != "" && !digestPattern.MatchString(t.AnswerDigest) {
		return errors.New("interaction tombstone digest is invalid")
	}
	if t.Phase != PhaseCompleted && t.Phase != PhaseCancelled && t.Phase != PhaseExpired {
		return errors.New("interaction tombstone phase is invalid")
	}
	if t.FinishedAt.IsZero() || t.FinishedAt.Location() != time.UTC || t.FinishedAt.Nanosecond() != 0 {
		return errors.New("interaction tombstone timestamp is invalid")
	}
	return nil
}

func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func DigestAnswer(answer Answer) (string, error) {
	canonical := answer
	canonical.Fields = append([]FieldAnswer(nil), answer.Fields...)
	for index := range canonical.Fields {
		field := &canonical.Fields[index]
		field.OptionIDs = append([]string(nil), field.OptionIDs...)
		sort.Strings(field.OptionIDs)
		if field.Freeform != nil {
			value := normalizeDigestText(strings.TrimSpace(*field.Freeform))
			field.Freeform = &value
		}
		if field.Text != nil {
			value := normalizeDigestText(*field.Text)
			field.Text = &value
		}
		if field.DateTime != nil {
			value := *field.DateTime
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				value = parsed.UTC().Format(time.RFC3339Nano)
			}
			field.DateTime = &value
		}
	}
	sort.Slice(canonical.Fields, func(i, j int) bool { return canonical.Fields[i].FieldID < canonical.Fields[j].FieldID })
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", errors.New("cannot encode normalized interaction answer")
	}
	return Digest(string(encoded)), nil
}

func normalizeDigestText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}
