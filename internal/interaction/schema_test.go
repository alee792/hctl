package interaction

import "testing"

func TestRequestJSONSchemaUsesContractVocabularyAndReturnsFreshValues(t *testing.T) {
	first := RequestJSONSchema()
	properties := first["properties"].(map[string]any)
	kinds := properties["kind"].(map[string]any)["enum"].([]string)
	wantKinds := []string{"confirm", "choose_one", "choose_many", "text", "date_time", "form"}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("kind vocabulary = %v", kinds)
	}
	for index := range wantKinds {
		if kinds[index] != wantKinds[index] {
			t.Fatalf("kind vocabulary = %v", kinds)
		}
	}
	properties["prompt"] = map[string]any{"type": "integer"}
	second := RequestJSONSchema()
	secondPrompt := second["properties"].(map[string]any)["prompt"].(map[string]any)
	if secondPrompt["type"] != "string" {
		t.Fatalf("schema values were shared across calls: %#v", secondPrompt)
	}
}
