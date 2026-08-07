package interaction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConformanceFixtures(t *testing.T) {
	kinds := []string{"confirm", "choose_one", "choose_many", "text", "date_time", "form"}
	for _, name := range kinds {
		t.Run(name, func(t *testing.T) {
			request := readRequestFixture(t, name)
			if request.Kind != Kind(name) {
				t.Fatalf("kind = %q", request.Kind)
			}

			valid := readAnswerFixture(t, name+".valid")
			normalized, err := NormalizeAnswer(request, valid)
			if err != nil {
				t.Fatalf("valid answer: %v", err)
			}
			if normalized.SchemaVersion != SchemaVersion || normalized.Action != ActionSubmit {
				t.Fatalf("normalized answer = %#v", normalized)
			}

			invalid := readAnswerFixture(t, name+".invalid")
			if _, err := NormalizeAnswer(request, invalid); err == nil {
				t.Fatal("invalid answer accepted")
			}
		})
	}
}

func TestNormalizeAnswerCanonicalizesValues(t *testing.T) {
	choice := readRequestFixture(t, "choose_many")
	answer := readAnswerFixture(t, "choose_many.valid")
	normalized, err := NormalizeAnswer(choice, answer)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := normalized.Fields[0].OptionIDs, []string{"lint", "race"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("option order = %v, want %v", got, want)
	}

	textRequest := readRequestFixture(t, "text")
	textAnswer := readAnswerFixture(t, "text.valid")
	normalized, err = NormalizeAnswer(textRequest, textAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if got := *normalized.Fields[0].Text; got != "Ready\nfor release" {
		t.Fatalf("normalized text = %q", got)
	}

	dateRequest := readRequestFixture(t, "date_time")
	dateAnswer := readAnswerFixture(t, "date_time.valid")
	normalized, err = NormalizeAnswer(dateRequest, dateAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if got := *normalized.Fields[0].DateTime; got != "2026-08-07T16:30:00Z" {
		t.Fatalf("normalized date_time = %q", got)
	}

	formRequest := readRequestFixture(t, "form")
	formAnswer := readAnswerFixture(t, "form.valid")
	normalized, err = NormalizeAnswer(formRequest, formAnswer)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := []string{normalized.Fields[0].FieldID, normalized.Fields[1].FieldID, normalized.Fields[2].FieldID}
	if want := []string{"environment", "release_date", "note"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("field order = %v, want %v", gotIDs, want)
	}
}

func TestNormalizeAnswerCancellation(t *testing.T) {
	request := readRequestFixture(t, "confirm")
	got, err := NormalizeAnswer(request, Answer{SchemaVersion: 1, Action: ActionCancel})
	if err != nil || got.Action != ActionCancel || len(got.Fields) != 0 {
		t.Fatalf("cancel = %#v, %v", got, err)
	}

	request = readRequestFixture(t, "choose_one")
	if _, err := NormalizeAnswer(request, Answer{SchemaVersion: 1, Action: ActionCancel}); err == nil {
		t.Fatal("forbidden cancellation accepted")
	}
}

func TestNormalizeAnswerFreeformAndDateRepresentations(t *testing.T) {
	request := readRequestFixture(t, "choose_one")
	freeform := "  qa-east  "
	normalized, err := NormalizeAnswer(request, Answer{
		SchemaVersion: SchemaVersion,
		Action:        ActionSubmit,
		Fields:        []FieldAnswer{{FieldID: "environment", Freeform: &freeform}},
	})
	if err != nil || normalized.Fields[0].Freeform == nil || *normalized.Fields[0].Freeform != "qa-east" {
		t.Fatalf("freeform = %#v, %v", normalized, err)
	}

	dateRequest := readRequestFixture(t, "date_time")
	for _, test := range []struct {
		representation DateTimeRepresentation
		input          string
		want           string
	}{
		{DateOnly, "2026-08-07", "2026-08-07"},
		{TimeOnly, "09:30", "09:30"},
		{DateTime, "2026-08-07T09:30:00-07:00", "2026-08-07T16:30:00Z"},
	} {
		dateRequest.Field.DateTimeRepresentation = test.representation
		input := test.input
		normalized, err := NormalizeAnswer(dateRequest, Answer{
			SchemaVersion: SchemaVersion,
			Action:        ActionSubmit,
			Fields:        []FieldAnswer{{FieldID: "release_at", DateTime: &input}},
		})
		if err != nil || normalized.Fields[0].DateTime == nil || *normalized.Fields[0].DateTime != test.want {
			t.Fatalf("%s = %#v, %v", test.representation, normalized, err)
		}
	}
}

func TestStrictDecodingRejectsRuntimeAndVendorFields(t *testing.T) {
	base := `{"schema_version":1,"kind":"confirm","prompt":"Proceed?","policy":{"expires_after_seconds":60,"cancellation":"allowed"},"field":{"id":"proceed","kind":"confirm","label":"Proceed","required":true}`
	for _, field := range []string{
		`"interaction_id":"owned-by-model"`,
		`"callback_id":"vendor-value"`,
		`"authorized_user_id":"123"`,
		`"continuation_id":"native-session"`,
		`"url":"https://example.invalid"`,
		`"discord_components":[]`,
		`"code":"run()"`,
		`"credential_ref":"secret"`,
	} {
		data := []byte(base + `,` + field + `}}`)
		if _, err := DecodeRequest(data); err == nil {
			t.Fatalf("accepted forbidden field %s", field)
		}
	}
	if _, err := DecodeAnswer([]byte(`{"schema_version":1,"action":"cancel","callback_id":"x"}`)); err == nil {
		t.Fatal("answer accepted vendor callback field")
	}
	if _, err := DecodeAnswer([]byte(`{"schema_version":1,"action":"cancel","action":"submit"}`)); err == nil {
		t.Fatal("answer accepted duplicate object field")
	}
	if _, err := DecodeRequest([]byte(base + `,"policy":{"expires_after_seconds":60,"expires_after_seconds":120,"cancellation":"allowed"}}`)); err == nil {
		t.Fatal("request accepted duplicate nested object field")
	}
}

func TestValidateRequestBoundsAndUnion(t *testing.T) {
	valid := readRequestFixture(t, "confirm")
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{"schema", func(r *Request) { r.SchemaVersion = 2 }},
		{"prompt", func(r *Request) { r.Prompt = strings.Repeat("p", MaxPromptBytes+1) }},
		{"fallback", func(r *Request) { r.FallbackText = strings.Repeat("f", MaxFallbackBytes+1) }},
		{"expiry low", func(r *Request) { r.Policy.ExpiresAfterSeconds = MinExpirySeconds - 1 }},
		{"expiry high", func(r *Request) { r.Policy.ExpiresAfterSeconds = MaxExpirySeconds + 1 }},
		{"cancel policy", func(r *Request) { r.Policy.Cancellation = "sometimes" }},
		{"field id", func(r *Request) { r.Field.ID = "123456789" }},
		{"mismatched union", func(r *Request) { r.Field.Kind = KindText }},
		{"unknown kind", func(r *Request) { r.Kind = "buttons" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			field := *valid.Field
			request.Field = &field
			test.mutate(&request)
			if err := ValidateRequest(request); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestValidateRequestChoiceAndFormBounds(t *testing.T) {
	request := readRequestFixture(t, "choose_one")
	request.Field.Options = append(request.Field.Options, request.Field.Options[0])
	if err := ValidateRequest(request); err == nil {
		t.Fatal("duplicate option accepted")
	}

	form := readRequestFixture(t, "form")
	form.Fields = append(form.Fields, form.Fields[0])
	if err := ValidateRequest(form); err == nil {
		t.Fatal("duplicate field accepted")
	}

	form = readRequestFixture(t, "form")
	form.Fields = append(form.Fields, make([]Field, MaxFields-len(form.Fields)+1)...)
	if err := ValidateRequest(form); err == nil {
		t.Fatal("oversized form accepted")
	}
}

func TestResolveNativeFallbackAndFailure(t *testing.T) {
	request := readRequestFixture(t, "choose_one")
	capabilities := fullCapabilities()
	resolution, err := Resolve(request, capabilities)
	if err != nil || resolution.Mode != RenderNative {
		t.Fatalf("native resolution = %#v, %v", resolution, err)
	}

	capabilities.Kinds = []Kind{KindText}
	resolution, err = Resolve(request, capabilities)
	if err != nil || resolution.Mode != RenderTextFallback || resolution.FallbackText != request.FallbackText || resolution.Reason != ReasonKind {
		t.Fatalf("fallback resolution = %#v, %v", resolution, err)
	}
	expectedData, err := os.ReadFile(filepath.Join("testdata", "resolutions", "choose_one.text_fallback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected Resolution
	if err := json.Unmarshal(expectedData, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolution, expected) {
		t.Fatalf("fallback resolution = %#v, want fixture %#v", resolution, expected)
	}

	request.FallbackText = ""
	if _, err := Resolve(request, capabilities); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported error = %v", err)
	}
}

func TestResolveUsesDeterministicAdapterLimits(t *testing.T) {
	request := readRequestFixture(t, "form")
	capabilities := fullCapabilities()
	capabilities.MaxFields = 2
	resolution, err := Resolve(request, capabilities)
	if err != nil || resolution.Mode != RenderTextFallback || resolution.Reason != ReasonFieldCount {
		t.Fatalf("field limit = %#v, %v", resolution, err)
	}

	request = readRequestFixture(t, "choose_one")
	capabilities = fullCapabilities()
	capabilities.SupportsFreeform = false
	resolution, err = Resolve(request, capabilities)
	if err != nil || resolution.Reason != ReasonFreeform {
		t.Fatalf("freeform limit = %#v, %v", resolution, err)
	}

	request = readRequestFixture(t, "date_time")
	capabilities = fullCapabilities()
	capabilities.DateTimeRepresentations = []DateTimeRepresentation{DateOnly}
	resolution, err = Resolve(request, capabilities)
	if err != nil || resolution.Reason != ReasonDateTimeRepresentation {
		t.Fatalf("date time limit = %#v, %v", resolution, err)
	}

	request = readRequestFixture(t, "choose_many")
	capabilities = fullCapabilities()
	capabilities.MaxSelections = 1
	resolution, err = Resolve(request, capabilities)
	if err != nil || resolution.Reason != ReasonSelectionCount {
		t.Fatalf("selection limit = %#v, %v", resolution, err)
	}
}

func TestResolveRequiresOnlyRelevantLimits(t *testing.T) {
	request := readRequestFixture(t, "confirm")
	request.FallbackText = ""
	capabilities := Capabilities{
		Kinds:           []Kind{KindConfirm},
		MaxRequestBytes: MaxRequestBytes,
		MaxPromptBytes:  MaxPromptBytes,
		MaxFields:       1,
		MaxLabelBytes:   MaxLabelBytes,
	}
	resolution, err := Resolve(request, capabilities)
	if err != nil || resolution.Mode != RenderNative {
		t.Fatalf("confirm-only adapter = %#v, %v", resolution, err)
	}
}

func fullCapabilities() Capabilities {
	return Capabilities{
		Kinds:                   []Kind{KindConfirm, KindChooseOne, KindChooseMany, KindText, KindDateTime, KindForm},
		MaxRequestBytes:         MaxRequestBytes,
		MaxPromptBytes:          MaxPromptBytes,
		MaxFields:               MaxFields,
		MaxOptionsPerField:      MaxOptionsPerField,
		MaxSelections:           MaxSelections,
		MaxTotalOptions:         MaxTotalOptions,
		MaxLabelBytes:           MaxLabelBytes,
		MaxDescriptionBytes:     MaxDescriptionBytes,
		MaxValueBytes:           MaxValueBytes,
		MaxTextRunes:            MaxTextRunes,
		SupportsFreeform:        true,
		DateTimeRepresentations: []DateTimeRepresentation{DateOnly, TimeOnly, DateTime},
	}
}

func readRequestFixture(t *testing.T, name string) Request {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "requests", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func readAnswerFixture(t *testing.T, name string) Answer {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "answers", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := DecodeAnswer(data)
	if err != nil {
		t.Fatal(err)
	}
	return answer
}
