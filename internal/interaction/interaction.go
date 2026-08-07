// Package interaction defines hctl's transport-neutral interactive request
// contract. It validates model-authored requests, normalizes transport answers,
// and decides whether an adapter can render a request natively or must use the
// request's authored text fallback.
package interaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion       = 1
	MaxRequestBytes     = 32 << 10
	MaxAnswerBytes      = 16 << 10
	MaxPromptBytes      = 2 << 10
	MaxFallbackBytes    = 4 << 10
	MaxFields           = 8
	MaxOptionsPerField  = 25
	MaxSelections       = MaxOptionsPerField + 1
	MaxTotalOptions     = 64
	MaxIDBytes          = 64
	MaxLabelBytes       = 100
	MaxDescriptionBytes = 300
	MaxValueBytes       = 256
	MaxTextRunes        = 4_000
	MinExpirySeconds    = 60
	MaxExpirySeconds    = 7 * 24 * 60 * 60
)

var semanticID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Kind string

const (
	KindConfirm    Kind = "confirm"
	KindChooseOne  Kind = "choose_one"
	KindChooseMany Kind = "choose_many"
	KindText       Kind = "text"
	KindDateTime   Kind = "date_time"
	KindForm       Kind = "form"
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

// Policy is model-authored timing behavior. The lifecycle converts the
// relative expiry to controller-owned absolute state when it accepts a request.
type Policy struct {
	ExpiresAfterSeconds int          `json:"expires_after_seconds"`
	Cancellation        Cancellation `json:"cancellation"`
}

// Request contains semantic content only. Interaction IDs, callback IDs,
// ownership, authorization, and continuation metadata are intentionally absent.
type Request struct {
	SchemaVersion int     `json:"schema_version"`
	Kind          Kind    `json:"kind"`
	Prompt        string  `json:"prompt"`
	FallbackText  string  `json:"fallback_text,omitempty"`
	Policy        Policy  `json:"policy"`
	Field         *Field  `json:"field,omitempty"`
	Fields        []Field `json:"fields,omitempty"`
}

// Field is the shared input vocabulary. A non-form request has exactly one
// field whose kind matches the request; a form has a bounded list of fields.
type Field struct {
	ID                     string                 `json:"id"`
	Kind                   Kind                   `json:"kind"`
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

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value"`
}

type AnswerAction string

const (
	ActionSubmit AnswerAction = "submit"
	ActionCancel AnswerAction = "cancel"
)

// Answer carries user-controlled semantic values. Every field is correlated by
// stable field and option IDs; it contains no adapter callback identifiers.
type Answer struct {
	SchemaVersion int           `json:"schema_version"`
	Action        AnswerAction  `json:"action"`
	Fields        []FieldAnswer `json:"fields,omitempty"`
}

type FieldAnswer struct {
	FieldID   string   `json:"field_id"`
	Confirmed *bool    `json:"confirmed,omitempty"`
	OptionIDs []string `json:"option_ids,omitempty"`
	Freeform  *string  `json:"freeform,omitempty"`
	Text      *string  `json:"text,omitempty"`
	DateTime  *string  `json:"date_time,omitempty"`
}

// Capabilities is advertised by an adapter. Limits are adapter limits, not
// model-authored layout requests; zero means the adapter does not support that
// dimension.
type Capabilities struct {
	Kinds []Kind
	// FormFieldKinds is intentionally distinct from Kinds: an adapter may
	// render a request kind at the top level without being able to embed that
	// same field kind in its native form primitive.
	FormFieldKinds          []Kind
	MaxRequestBytes         int
	MaxPromptBytes          int
	MaxFields               int
	MaxOptionsPerField      int
	MaxSelections           int
	MaxTotalOptions         int
	MaxLabelBytes           int
	MaxDescriptionBytes     int
	MaxValueBytes           int
	MaxTextRunes            int
	SupportsFreeform        bool
	DateTimeRepresentations []DateTimeRepresentation
}

type RenderMode string

const (
	RenderNative       RenderMode = "native"
	RenderTextFallback RenderMode = "text_fallback"
)

// Resolution is the deterministic output of capability negotiation. Reason is
// stable machine-readable text suitable for tests and redacted diagnostics.
type Resolution struct {
	Mode         RenderMode        `json:"mode"`
	FallbackText string            `json:"fallback_text,omitempty"`
	Reason       DegradationReason `json:"reason,omitempty"`
}

type DegradationReason string

const (
	ReasonKind                   DegradationReason = "kind"
	ReasonFormFieldKind          DegradationReason = "form_field_kind"
	ReasonRequestSize            DegradationReason = "request_size"
	ReasonPromptSize             DegradationReason = "prompt_size"
	ReasonFieldCount             DegradationReason = "field_count"
	ReasonLabelSize              DegradationReason = "label_size"
	ReasonDescriptionSize        DegradationReason = "description_size"
	ReasonOptionCount            DegradationReason = "option_count"
	ReasonSelectionCount         DegradationReason = "selection_count"
	ReasonTotalOptionCount       DegradationReason = "total_option_count"
	ReasonValueSize              DegradationReason = "value_size"
	ReasonFreeform               DegradationReason = "freeform"
	ReasonTextSize               DegradationReason = "text_size"
	ReasonDateTimeRepresentation DegradationReason = "date_time_representation"
)

var ErrUnsupported = errors.New("interactive request is unsupported by adapter")

func DecodeRequest(data []byte) (Request, error) {
	var request Request
	if err := decode(data, MaxRequestBytes, &request); err != nil {
		return Request{}, fmt.Errorf("invalid interactive request: %w", err)
	}
	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func DecodeAnswer(data []byte) (Answer, error) {
	var answer Answer
	if err := decode(data, MaxAnswerBytes, &answer); err != nil {
		return Answer{}, fmt.Errorf("invalid interactive answer: %w", err)
	}
	return answer, nil
}

func decode(data []byte, limit int, target any) error {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return errors.New("input must be bounded UTF-8 JSON")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("input does not match the contract")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("input must contain one JSON value")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("input does not match the contract")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return errors.New("input does not match the contract")
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("input does not match the contract")
				}
				if _, exists := seen[key]; exists {
					return errors.New("input repeats an object field")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("input does not match the contract")
		}
		if _, err := decoder.Token(); err != nil {
			return errors.New("input does not match the contract")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("input must contain one JSON value")
	}
	return nil
}

func ValidateRequest(request Request) error {
	if request.SchemaVersion != SchemaVersion {
		return errors.New("interactive request schema_version must be 1")
	}
	if !boundedMultilineText(request.Prompt, 1, MaxPromptBytes) {
		return errors.New("interactive request prompt is invalid")
	}
	if request.FallbackText != "" && !boundedMultilineText(request.FallbackText, 1, MaxFallbackBytes) {
		return errors.New("interactive request fallback_text is invalid")
	}
	if request.Policy.ExpiresAfterSeconds < MinExpirySeconds || request.Policy.ExpiresAfterSeconds > MaxExpirySeconds {
		return errors.New("interactive request expiry is invalid")
	}
	if request.Policy.Cancellation != CancellationAllowed && request.Policy.Cancellation != CancellationForbidden {
		return errors.New("interactive request cancellation policy is invalid")
	}
	if !validKind(request.Kind, true) {
		return errors.New("interactive request kind is invalid")
	}

	fields := request.Fields
	if request.Kind == KindForm {
		if request.Field != nil || len(fields) < 1 || len(fields) > MaxFields {
			return errors.New("form request must contain a bounded fields list")
		}
	} else {
		if request.Field == nil || len(fields) != 0 || request.Field.Kind != request.Kind || !request.Field.Required {
			return errors.New("single-field request must contain one required matching field")
		}
		fields = []Field{*request.Field}
	}

	fieldIDs := make(map[string]struct{}, len(fields))
	totalOptions := 0
	for _, field := range fields {
		if err := validateField(field); err != nil {
			return fmt.Errorf("interactive request field is invalid: %w", err)
		}
		if _, exists := fieldIDs[field.ID]; exists {
			return fmt.Errorf("interactive request repeats field ID %q", field.ID)
		}
		fieldIDs[field.ID] = struct{}{}
		totalOptions += len(field.Options)
	}
	if totalOptions > MaxTotalOptions {
		return errors.New("interactive request has too many total options")
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		return errors.New("interactive request exceeds the encoded size limit")
	}
	return nil
}

func validateField(field Field) error {
	if !semanticID.MatchString(field.ID) || len(field.ID) > MaxIDBytes {
		return errors.New("field ID is invalid")
	}
	if !validKind(field.Kind, false) {
		return errors.New("field kind is invalid")
	}
	if !boundedText(field.Label, 1, MaxLabelBytes) || (field.Description != "" && !boundedText(field.Description, 1, MaxDescriptionBytes)) {
		return errors.New("field label or description is invalid")
	}

	switch field.Kind {
	case KindConfirm:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.MinLength != 0 || field.MaxLength != 0 || field.DateTimeRepresentation != "" {
			return errors.New("confirm field contains inapplicable constraints")
		}
	case KindChooseOne, KindChooseMany:
		if len(field.Options) < 1 || len(field.Options) > MaxOptionsPerField || field.DateTimeRepresentation != "" {
			return errors.New("choice field options are invalid")
		}
		if field.AllowFreeform {
			if field.MinLength < 0 || field.MaxLength < 1 || field.MaxLength > MaxTextRunes || field.MinLength > field.MaxLength {
				return errors.New("choice freeform bounds are invalid")
			}
		} else if field.MinLength != 0 || field.MaxLength != 0 {
			return errors.New("choice field has freeform bounds without freeform input")
		}
		maximum := len(field.Options)
		if field.AllowFreeform {
			maximum++
		}
		if field.Kind == KindChooseOne {
			if field.MaxSelections != 1 || field.MinSelections < 0 || field.MinSelections > 1 || (field.Required && field.MinSelections != 1) {
				return errors.New("choose_one cardinality is invalid")
			}
		} else if field.MinSelections < 0 || field.MaxSelections < 1 || field.MaxSelections > maximum || field.MinSelections > field.MaxSelections || (field.Required && field.MinSelections < 1) {
			return errors.New("choose_many cardinality is invalid")
		}
		if err := validateOptions(field.Options); err != nil {
			return err
		}
	case KindText:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.DateTimeRepresentation != "" || field.MinLength < 0 || field.MaxLength < 1 || field.MaxLength > MaxTextRunes || field.MinLength > field.MaxLength || (field.Required && field.MinLength < 1) {
			return errors.New("text field constraints are invalid")
		}
	case KindDateTime:
		if len(field.Options) != 0 || field.AllowFreeform || field.MinSelections != 0 || field.MaxSelections != 0 || field.MinLength != 0 || field.MaxLength != 0 || !validDateTimeRepresentation(field.DateTimeRepresentation) {
			return errors.New("date_time field constraints are invalid")
		}
	}
	return nil
}

func validateOptions(options []Option) error {
	ids := make(map[string]struct{}, len(options))
	for _, option := range options {
		if !semanticID.MatchString(option.ID) || len(option.ID) > MaxIDBytes || !boundedText(option.Label, 1, MaxLabelBytes) || !boundedText(option.Value, 1, MaxValueBytes) || (option.Description != "" && !boundedText(option.Description, 1, MaxDescriptionBytes)) {
			return errors.New("choice option is invalid")
		}
		if _, exists := ids[option.ID]; exists {
			return fmt.Errorf("choice repeats option ID %q", option.ID)
		}
		ids[option.ID] = struct{}{}
	}
	return nil
}

// NormalizeAnswer validates an answer against the exact request and returns a
// canonical answer ordered by request fields and options.
func NormalizeAnswer(request Request, answer Answer) (Answer, error) {
	if err := ValidateRequest(request); err != nil {
		return Answer{}, err
	}
	if answer.SchemaVersion != SchemaVersion {
		return Answer{}, errors.New("interactive answer schema_version must be 1")
	}
	if encoded, err := json.Marshal(answer); err != nil || len(encoded) > MaxAnswerBytes {
		return Answer{}, errors.New("interactive answer exceeds the encoded size limit")
	}
	if answer.Action == ActionCancel {
		if request.Policy.Cancellation != CancellationAllowed || len(answer.Fields) != 0 {
			return Answer{}, errors.New("interactive request cannot be cancelled with this answer")
		}
		return Answer{SchemaVersion: SchemaVersion, Action: ActionCancel}, nil
	}
	if answer.Action != ActionSubmit {
		return Answer{}, errors.New("interactive answer action is invalid")
	}

	fields := request.Fields
	if request.Field != nil {
		fields = []Field{*request.Field}
	}
	byID := make(map[string]FieldAnswer, len(answer.Fields))
	for _, value := range answer.Fields {
		if !semanticID.MatchString(value.FieldID) {
			return Answer{}, errors.New("interactive answer contains an invalid field ID")
		}
		if _, exists := byID[value.FieldID]; exists {
			return Answer{}, fmt.Errorf("interactive answer repeats field ID %q", value.FieldID)
		}
		byID[value.FieldID] = value
	}

	normalized := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit}
	for _, field := range fields {
		value, present := byID[field.ID]
		if !present {
			if field.Required {
				return Answer{}, fmt.Errorf("interactive answer omits required field %q", field.ID)
			}
			continue
		}
		delete(byID, field.ID)
		value, err := normalizeFieldAnswer(field, value)
		if err != nil {
			return Answer{}, fmt.Errorf("field %q: %w", field.ID, err)
		}
		normalized.Fields = append(normalized.Fields, value)
	}
	if len(byID) != 0 {
		return Answer{}, errors.New("interactive answer contains an unknown field ID")
	}
	return normalized, nil
}

func normalizeFieldAnswer(field Field, answer FieldAnswer) (FieldAnswer, error) {
	result := FieldAnswer{FieldID: field.ID}
	switch field.Kind {
	case KindConfirm:
		if answer.Confirmed == nil || hasChoiceOrText(answer) {
			return FieldAnswer{}, errors.New("confirm answer is invalid")
		}
		value := *answer.Confirmed
		result.Confirmed = &value
	case KindChooseOne, KindChooseMany:
		if answer.Confirmed != nil || answer.Text != nil || answer.DateTime != nil {
			return FieldAnswer{}, errors.New("choice answer is invalid")
		}
		seen := make(map[string]struct{}, len(answer.OptionIDs))
		for _, id := range answer.OptionIDs {
			if _, exists := seen[id]; exists {
				return FieldAnswer{}, fmt.Errorf("choice answer repeats option ID %q", id)
			}
			seen[id] = struct{}{}
		}
		for _, option := range field.Options {
			if _, selected := seen[option.ID]; selected {
				result.OptionIDs = append(result.OptionIDs, option.ID)
				delete(seen, option.ID)
			}
		}
		if len(seen) != 0 {
			return FieldAnswer{}, errors.New("choice answer contains an unknown option ID")
		}
		count := len(result.OptionIDs)
		if answer.Freeform != nil {
			if !field.AllowFreeform {
				return FieldAnswer{}, errors.New("choice answer does not allow freeform input")
			}
			value := normalizeLineEndings(strings.TrimSpace(*answer.Freeform))
			if !boundedTextRunes(value, field.MinLength, field.MaxLength) {
				return FieldAnswer{}, errors.New("choice freeform answer is outside its bounds")
			}
			result.Freeform = &value
			count++
		}
		if count < field.MinSelections || count > field.MaxSelections {
			return FieldAnswer{}, errors.New("choice answer violates selection cardinality")
		}
	case KindText:
		if answer.Text == nil || answer.Confirmed != nil || len(answer.OptionIDs) != 0 || answer.Freeform != nil || answer.DateTime != nil {
			return FieldAnswer{}, errors.New("text answer is invalid")
		}
		value := normalizeLineEndings(*answer.Text)
		if !boundedTextRunes(value, field.MinLength, field.MaxLength) {
			return FieldAnswer{}, errors.New("text answer is outside its bounds")
		}
		result.Text = &value
	case KindDateTime:
		if answer.DateTime == nil || answer.Confirmed != nil || len(answer.OptionIDs) != 0 || answer.Freeform != nil || answer.Text != nil {
			return FieldAnswer{}, errors.New("date_time answer is invalid")
		}
		value, err := normalizeDateTime(*answer.DateTime, field.DateTimeRepresentation)
		if err != nil {
			return FieldAnswer{}, err
		}
		result.DateTime = &value
	}
	return result, nil
}

func hasChoiceOrText(answer FieldAnswer) bool {
	return len(answer.OptionIDs) != 0 || answer.Freeform != nil || answer.Text != nil || answer.DateTime != nil
}

func normalizeDateTime(value string, representation DateTimeRepresentation) (string, error) {
	var parsed time.Time
	var err error
	switch representation {
	case DateOnly:
		parsed, err = time.Parse("2006-01-02", value)
		if err == nil {
			return parsed.Format("2006-01-02"), nil
		}
	case TimeOnly:
		parsed, err = time.Parse("15:04", value)
		if err == nil {
			return parsed.Format("15:04"), nil
		}
	case DateTime:
		parsed, err = time.Parse(time.RFC3339, value)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", errors.New("date_time answer does not match its representation")
}

// Resolve checks adapter capabilities before lifecycle state is created.
func Resolve(request Request, capabilities Capabilities) (Resolution, error) {
	if err := ValidateRequest(request); err != nil {
		return Resolution{}, err
	}
	reason := unsupportedReason(request, capabilities)
	if reason == "" {
		return Resolution{Mode: RenderNative}, nil
	}
	if request.FallbackText != "" {
		return Resolution{Mode: RenderTextFallback, FallbackText: request.FallbackText, Reason: reason}, nil
	}
	return Resolution{}, fmt.Errorf("%w: %s", ErrUnsupported, reason)
}

func unsupportedReason(request Request, capabilities Capabilities) DegradationReason {
	supported := make(map[Kind]bool, len(capabilities.Kinds))
	for _, kind := range capabilities.Kinds {
		supported[kind] = true
	}
	if !supported[request.Kind] {
		return ReasonKind
	}
	fields := request.Fields
	if request.Field != nil {
		fields = []Field{*request.Field}
	}
	if request.Kind == KindForm {
		formSupported := make(map[Kind]bool, len(capabilities.FormFieldKinds))
		for _, kind := range capabilities.FormFieldKinds {
			formSupported[kind] = true
		}
		for _, field := range fields {
			if !formSupported[field.Kind] {
				return ReasonFormFieldKind
			}
		}
	}
	encoded, _ := json.Marshal(request)
	checks := []struct {
		exceeded bool
		reason   DegradationReason
	}{
		{len(encoded) > limit(capabilities.MaxRequestBytes), ReasonRequestSize},
		{len([]byte(request.Prompt)) > limit(capabilities.MaxPromptBytes), ReasonPromptSize},
		{len(fields) > limit(capabilities.MaxFields), ReasonFieldCount},
	}
	for _, check := range checks {
		if check.exceeded {
			return check.reason
		}
	}
	totalOptions := 0
	for _, field := range fields {
		if len([]byte(field.Label)) > limit(capabilities.MaxLabelBytes) {
			return ReasonLabelSize
		}
		if field.Description != "" && len([]byte(field.Description)) > limit(capabilities.MaxDescriptionBytes) {
			return ReasonDescriptionSize
		}
		if len(field.Options) > 0 && len(field.Options) > limit(capabilities.MaxOptionsPerField) {
			return ReasonOptionCount
		}
		if (field.Kind == KindChooseOne || field.Kind == KindChooseMany) && field.MaxSelections > limit(capabilities.MaxSelections) {
			return ReasonSelectionCount
		}
		totalOptions += len(field.Options)
		for _, option := range field.Options {
			if len([]byte(option.Label)) > limit(capabilities.MaxLabelBytes) {
				return ReasonLabelSize
			}
			if option.Description != "" && len([]byte(option.Description)) > limit(capabilities.MaxDescriptionBytes) {
				return ReasonDescriptionSize
			}
			if len([]byte(option.Value)) > limit(capabilities.MaxValueBytes) {
				return ReasonValueSize
			}
		}
		if field.AllowFreeform && !capabilities.SupportsFreeform {
			return ReasonFreeform
		}
		if (field.Kind == KindText || field.AllowFreeform) && field.MaxLength > limit(capabilities.MaxTextRunes) {
			return ReasonTextSize
		}
		if field.Kind == KindDateTime && !containsRepresentation(capabilities.DateTimeRepresentations, field.DateTimeRepresentation) {
			return ReasonDateTimeRepresentation
		}
	}
	if totalOptions > 0 && totalOptions > limit(capabilities.MaxTotalOptions) {
		return ReasonTotalOptionCount
	}
	return ""
}

// zero is unsupported rather than unbounded. The helper keeps comparisons
// readable while ensuring malformed negative limits also reject all requests.
func limit(value int) int {
	if value <= 0 {
		return -1
	}
	return value
}

func validKind(kind Kind, allowForm bool) bool {
	switch kind {
	case KindConfirm, KindChooseOne, KindChooseMany, KindText, KindDateTime:
		return true
	case KindForm:
		return allowForm
	default:
		return false
	}
}

func validDateTimeRepresentation(value DateTimeRepresentation) bool {
	return value == DateOnly || value == TimeOnly || value == DateTime
}

func containsRepresentation(values []DateTimeRepresentation, target DateTimeRepresentation) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func boundedText(value string, minBytes, maxBytes int) bool {
	return utf8.ValidString(value) && len([]byte(value)) >= minBytes && len([]byte(value)) <= maxBytes && strings.TrimSpace(value) == value && !containsControl(value)
}

func boundedMultilineText(value string, minBytes, maxBytes int) bool {
	return utf8.ValidString(value) && len([]byte(value)) >= minBytes && len([]byte(value)) <= maxBytes && strings.TrimSpace(value) == value && !containsControlExceptNewline(value)
}

func boundedTextRunes(value string, minRunes, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= minRunes && utf8.RuneCountInString(value) <= maxRunes && !containsControlExceptNewline(value)
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func containsControlExceptNewline(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

func normalizeLineEndings(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}
