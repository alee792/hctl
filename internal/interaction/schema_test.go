package interaction

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestRequestJSONSchemaDiscriminatesEveryRequestAndFieldKind(t *testing.T) {
	schema := RequestJSONSchema()
	branches := schema["oneOf"].([]any)
	wantKinds := []Kind{KindConfirm, KindChooseOne, KindChooseMany, KindText, KindDateTime, KindForm}
	if len(branches) != len(wantKinds) {
		t.Fatalf("request branches = %d, want %d", len(branches), len(wantKinds))
	}
	for _, kind := range wantKinds {
		branch := schemaBranchForKind(t, branches, kind)
		if branch["additionalProperties"] != false {
			t.Fatalf("%s request accepts undeclared properties", kind)
		}
		required := branch["required"].([]string)
		container := "field"
		if kind == KindForm {
			container = "fields"
		}
		if !slices.Contains(required, container) {
			t.Fatalf("%s request required = %v", kind, required)
		}
	}

	form := schemaBranchForKind(t, branches, KindForm)
	items := form["properties"].(map[string]any)["fields"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)
	if len(items) != 5 {
		t.Fatalf("form field branches = %d", len(items))
	}
	for _, kind := range wantKinds[:5] {
		assertFieldSchemaConforms(t, schemaBranchForKind(t, items, kind), kind, false)
		request := schemaBranchForKind(t, branches, kind)
		field := request["properties"].(map[string]any)["field"].(map[string]any)
		assertFieldSchemaConforms(t, field, kind, true)
	}
}

func TestRequestJSONSchemaRequiresRuntimeChoiceAndTextConstraints(t *testing.T) {
	branches := RequestJSONSchema()["oneOf"].([]any)
	chooseOne := schemaBranchForKind(t, branches, KindChooseOne)["properties"].(map[string]any)["field"].(map[string]any)
	assertRequired(t, chooseOne, "options", "min_selections", "max_selections")
	choiceProperties := chooseOne["properties"].(map[string]any)
	if choiceProperties["min_selections"].(map[string]any)["const"] != 1 || choiceProperties["max_selections"].(map[string]any)["const"] != 1 {
		t.Fatalf("choose_one cardinality is not exact: %#v", choiceProperties)
	}
	if choiceProperties["options"].(map[string]any)["minItems"] != 1 {
		t.Fatalf("choose_one permits an empty options list: %#v", choiceProperties["options"])
	}

	chooseMany := schemaBranchForKind(t, branches, KindChooseMany)["properties"].(map[string]any)["field"].(map[string]any)
	assertRequired(t, chooseMany, "options", "min_selections", "max_selections")
	text := schemaBranchForKind(t, branches, KindText)["properties"].(map[string]any)["field"].(map[string]any)
	assertRequired(t, text, "min_length", "max_length")
	dateTime := schemaBranchForKind(t, branches, KindDateTime)["properties"].(map[string]any)["field"].(map[string]any)
	assertRequired(t, dateTime, "date_time_representation")
}

func TestUnderconstrainedChooseOneIsRejectedByRuntimeContract(t *testing.T) {
	underconstrained := []byte(`{
		"schema_version":1,
		"kind":"choose_one",
		"prompt":"Choose an environment",
		"policy":{"expires_after_seconds":60,"cancellation":"allowed"},
		"field":{"id":"environment","kind":"choose_one","label":"Environment","required":true}
	}`)
	if _, err := DecodeRequest(underconstrained); err == nil {
		t.Fatal("underconstrained choose_one request was accepted")
	}
}

func TestRequestJSONSchemaReturnsFreshValues(t *testing.T) {
	first := RequestJSONSchema()
	firstBranch := schemaBranchForKind(t, first["oneOf"].([]any), KindConfirm)
	firstBranch["properties"].(map[string]any)["prompt"] = map[string]any{"type": "integer"}
	second := RequestJSONSchema()
	secondBranch := schemaBranchForKind(t, second["oneOf"].([]any), KindConfirm)
	secondPrompt := secondBranch["properties"].(map[string]any)["prompt"].(map[string]any)
	if secondPrompt["type"] != "string" {
		t.Fatalf("schema values were shared across calls: %#v", secondPrompt)
	}
}

func assertFieldSchemaConforms(t *testing.T, field map[string]any, kind Kind, single bool) {
	t.Helper()
	properties := field["properties"].(map[string]any)
	if got := properties["kind"].(map[string]any)["const"]; got != string(kind) {
		t.Fatalf("field discriminator = %v, want %s", got, kind)
	}
	if field["additionalProperties"] != false {
		t.Fatalf("%s field accepts undeclared properties", kind)
	}
	assertRequired(t, field, "id", "kind", "label", "required")
	if single && properties["required"].(map[string]any)["const"] != true {
		t.Fatalf("single %s field is not required", kind)
	}
	switch kind {
	case KindChooseOne, KindChooseMany:
		assertRequired(t, field, "options", "min_selections", "max_selections")
	case KindText:
		assertRequired(t, field, "min_length", "max_length")
	case KindDateTime:
		assertRequired(t, field, "date_time_representation")
	}
	allowed := map[Kind][]string{
		KindConfirm:    {"id", "kind", "label", "description", "required"},
		KindChooseOne:  {"id", "kind", "label", "description", "required", "options", "allow_freeform", "min_selections", "max_selections", "min_length", "max_length"},
		KindChooseMany: {"id", "kind", "label", "description", "required", "options", "allow_freeform", "min_selections", "max_selections", "min_length", "max_length"},
		KindText:       {"id", "kind", "label", "description", "required", "min_length", "max_length"},
		KindDateTime:   {"id", "kind", "label", "description", "required", "date_time_representation"},
	}
	if len(properties) != len(allowed[kind]) {
		t.Fatalf("%s properties = %v, want only %v", kind, mapKeys(properties), allowed[kind])
	}
	for _, name := range allowed[kind] {
		if _, ok := properties[name]; !ok {
			t.Fatalf("%s schema omits %q", kind, name)
		}
	}
}

func schemaBranchForKind(t *testing.T, branches []any, kind Kind) map[string]any {
	t.Helper()
	for _, candidate := range branches {
		branch := candidate.(map[string]any)
		properties := branch["properties"].(map[string]any)
		if properties["kind"].(map[string]any)["const"] == string(kind) {
			return branch
		}
	}
	t.Fatalf("schema has no %s branch; schema=%s", kind, mustJSON(branches))
	return nil
}

func assertRequired(t *testing.T, schema map[string]any, names ...string) {
	t.Helper()
	required := schema["required"].([]string)
	for _, name := range names {
		if !slices.Contains(required, name) {
			t.Fatalf("required = %v; missing %q", required, name)
		}
	}
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
