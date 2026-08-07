package interaction

import (
	"strings"
	"testing"
	"time"
)

func TestLifecycleValidate(t *testing.T) {
	request := lifecycleTestRequest()
	lifecycle := Lifecycle{
		ID:           "interaction_1234567890",
		InputID:      "m1",
		Owner:        Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request:      request,
		Resolution:   Resolution{Mode: RenderNative},
		ExpiresAt:    time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
		Continuation: ContinuationTurn,
		Phase:        PhaseRequested,
		Delivery:     DeliveryPending,
	}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}

	answer, err := NormalizeAnswer(request, Answer{
		SchemaVersion: SchemaVersion,
		Action:        ActionSubmit,
		Fields:        []FieldAnswer{{FieldID: "approved", Confirmed: boolPointer(true)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := DigestAnswer(answer)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Phase = PhaseAnswered
	lifecycle.Delivery = DeliveryDelivered
	lifecycle.Answer = &answer
	lifecycle.AnswerDigest = digest
	lifecycle.Resume = ResumePending
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	lifecycle.Delivery = DeliveryUncertain
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("answered interaction retained uncertain delivery after authenticated proof")
	}
}

func TestLifecycleRejectsImpossibleState(t *testing.T) {
	request := lifecycleTestRequest()
	lifecycle := Lifecycle{
		ID:           "interaction_1234567890",
		InputID:      "input_1234567890123",
		Owner:        Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request:      request,
		Resolution:   Resolution{Mode: RenderNative},
		ExpiresAt:    time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
		Continuation: ContinuationTurn,
		Phase:        PhaseRendered,
		Delivery:     DeliveryIntended,
	}
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("rendered interaction with incomplete delivery was accepted")
	}
	lifecycle.Phase = PhaseRequested
	lifecycle.Delivery = DeliveryDelivered
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("requested interaction with completed delivery was accepted")
	}

	lifecycle.Phase = PhaseRequested
	lifecycle.Delivery = DeliveryPending
	lifecycle.Owner.SurfaceKey = "raw-discord-id"
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("raw ownership key was accepted")
	}
}

func TestLifecycleRejectsMismatchedAnswerDigestAndUnsafeContinuation(t *testing.T) {
	request := lifecycleTestRequest()
	answer, err := NormalizeAnswer(request, Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{{FieldID: "approved", Confirmed: boolPointer(true)}}})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := Lifecycle{
		ID: "interaction_1234567890", InputID: "m1",
		Owner:   Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request: request, Resolution: Resolution{Mode: RenderNative}, ExpiresAt: time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
		Continuation: ContinuationTurn, Phase: PhaseAnswered, Delivery: DeliveryDelivered,
		Answer: &answer, AnswerDigest: Digest("different"), Resume: ResumePending,
	}
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("mismatched answer digest was accepted")
	}
	lifecycle.Phase, lifecycle.Delivery, lifecycle.Answer, lifecycle.AnswerDigest, lifecycle.Resume = PhaseRequested, DeliveryPending, nil, "", ""
	lifecycle.Continuation = ContinuationNativeDeferredTool
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("native deferral without a continuation key was accepted")
	}
	lifecycle.ContinuationKey = "tool\nkey"
	if err := lifecycle.Validate(); err == nil {
		t.Fatal("control character in continuation key was accepted")
	}
}

func TestTombstoneContainsOnlyDigests(t *testing.T) {
	tombstone := Tombstone{
		InteractionDigest: Digest("interaction_1234567890"),
		OwnerDigest:       Digest("owner"),
		AnswerDigest:      Digest("answer"),
		Phase:             PhaseCompleted,
		FinishedAt:        time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
	}
	if err := tombstone.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDigestAnswerIsIndependentOfSemanticOrderingAndNormalization(t *testing.T) {
	textCRLF := "line one\r\nline two"
	textLF := "line one\nline two"
	offset := "2026-08-07T01:00:00-07:00"
	utc := "2026-08-07T08:00:00Z"
	first := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{
		{FieldID: "when", DateTime: &offset},
		{FieldID: "details", Text: &textCRLF, OptionIDs: []string{"second", "first"}},
	}}
	second := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{
		{FieldID: "details", Text: &textLF, OptionIDs: []string{"first", "second"}},
		{FieldID: "when", DateTime: &utc},
	}}
	firstDigest, err := DigestAnswer(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := DigestAnswer(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent answer digests differ: %s != %s", firstDigest, secondDigest)
	}
}

func lifecycleTestRequest() Request {
	return Request{
		SchemaVersion: SchemaVersion,
		Kind:          KindConfirm,
		Prompt:        "Proceed?",
		Policy:        Policy{ExpiresAfterSeconds: MinExpirySeconds, Cancellation: CancellationAllowed},
		Field:         &Field{ID: "approved", Kind: KindConfirm, Label: "Proceed", Required: true},
	}
}

func boolPointer(value bool) *bool { return &value }
