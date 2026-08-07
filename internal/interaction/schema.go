package interaction

// RequestJSONSchema returns a fresh JSON Schema for the model-authored semantic
// request. Each request kind has its own branch so the advertised contract does
// not offer fields that runtime validation will reject as inapplicable.
// Runtime decoding and validation remain authoritative for constraints JSON
// Schema cannot express, such as selection counts bounded by option count.
func RequestJSONSchema() map[string]any {
	branches := make([]any, 0, 6)
	for _, kind := range []Kind{KindConfirm, KindChooseOne, KindChooseMany, KindText, KindDateTime} {
		branches = append(branches, requestBranch(kind, singleFieldSchema(kind)))
	}
	branches = append(branches, formRequestBranch())
	return map[string]any{
		"type":  "object",
		"oneOf": branches,
	}
}

func requestBranch(kind Kind, field map[string]any) map[string]any {
	properties := requestProperties(kind)
	properties["field"] = field
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             []string{"schema_version", "kind", "prompt", "policy", "field"},
	}
}

func formRequestBranch() map[string]any {
	fieldBranches := make([]any, 0, 5)
	for _, kind := range []Kind{KindConfirm, KindChooseOne, KindChooseMany, KindText, KindDateTime} {
		fieldBranches = append(fieldBranches, formFieldSchema(kind))
	}
	properties := requestProperties(KindForm)
	properties["fields"] = map[string]any{
		"type": "array", "minItems": 1, "maxItems": MaxFields,
		"items": map[string]any{"oneOf": fieldBranches},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             []string{"schema_version", "kind", "prompt", "policy", "fields"},
	}
}

func requestProperties(kind Kind) map[string]any {
	return map[string]any{
		"schema_version": map[string]any{"type": "integer", "const": SchemaVersion},
		"kind":           map[string]any{"type": "string", "const": string(kind)},
		"prompt":         map[string]any{"type": "string", "minLength": 1, "maxLength": MaxPromptBytes},
		"fallback_text":  map[string]any{"type": "string", "maxLength": MaxFallbackBytes},
		"policy": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"expires_after_seconds": map[string]any{"type": "integer", "minimum": MinExpirySeconds, "maximum": MaxExpirySeconds},
				"cancellation":          map[string]any{"type": "string", "enum": []string{string(CancellationAllowed), string(CancellationForbidden)}},
			},
			"required": []string{"expires_after_seconds", "cancellation"},
		},
	}
}

func singleFieldSchema(kind Kind) map[string]any {
	field := fieldSchema(kind, map[string]any{"type": "boolean", "const": true})
	properties := field["properties"].(map[string]any)
	required := field["required"].([]string)
	switch kind {
	case KindChooseOne:
		properties["min_selections"] = map[string]any{"type": "integer", "const": 1}
		properties["max_selections"] = map[string]any{"type": "integer", "const": 1}
		required = append(required, "options", "min_selections", "max_selections")
	case KindChooseMany:
		properties["min_selections"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxSelections}
		properties["max_selections"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxSelections}
		required = append(required, "options", "min_selections", "max_selections")
	case KindText:
		properties["min_length"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTextRunes}
		properties["max_length"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTextRunes}
		required = append(required, "min_length", "max_length")
	case KindDateTime:
		required = append(required, "date_time_representation")
	}
	field["required"] = required
	return field
}

func formFieldSchema(kind Kind) map[string]any {
	field := fieldSchema(kind, map[string]any{"type": "boolean"})
	properties := field["properties"].(map[string]any)
	required := field["required"].([]string)
	switch kind {
	case KindChooseOne:
		properties["min_selections"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 1}
		properties["max_selections"] = map[string]any{"type": "integer", "const": 1}
		required = append(required, "options", "min_selections", "max_selections")
		field["allOf"] = appendConstraint(field["allOf"], requiredMinimumWhenRequired("min_selections", 1))
	case KindChooseMany:
		properties["min_selections"] = map[string]any{"type": "integer", "minimum": 0, "maximum": MaxSelections}
		properties["max_selections"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxSelections}
		required = append(required, "options", "min_selections", "max_selections")
		field["allOf"] = appendConstraint(field["allOf"], requiredMinimumWhenRequired("min_selections", 1))
	case KindText:
		properties["min_length"] = map[string]any{"type": "integer", "minimum": 0, "maximum": MaxTextRunes}
		properties["max_length"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTextRunes}
		required = append(required, "min_length", "max_length")
		field["allOf"] = appendConstraint(field["allOf"], requiredMinimumWhenRequired("min_length", 1))
	case KindDateTime:
		required = append(required, "date_time_representation")
	}
	field["required"] = required
	return field
}

func fieldSchema(kind Kind, requiredProperty map[string]any) map[string]any {
	properties := map[string]any{
		"id":          map[string]any{"type": "string", "pattern": semanticID.String()},
		"kind":        map[string]any{"type": "string", "const": string(kind)},
		"label":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxLabelBytes},
		"description": map[string]any{"type": "string", "maxLength": MaxDescriptionBytes},
		"required":    requiredProperty,
	}
	field := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             []string{"id", "kind", "label", "required"},
	}
	switch kind {
	case KindChooseOne, KindChooseMany:
		properties["options"] = map[string]any{"type": "array", "minItems": 1, "maxItems": MaxOptionsPerField, "items": optionSchema()}
		properties["allow_freeform"] = map[string]any{"type": "boolean"}
		properties["min_length"] = map[string]any{"type": "integer", "minimum": 0, "maximum": MaxTextRunes}
		properties["max_length"] = map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTextRunes}
		field["allOf"] = []any{freeformBoundsConstraint()}
	case KindDateTime:
		properties["date_time_representation"] = map[string]any{"type": "string", "enum": []string{string(DateOnly), string(TimeOnly), string(DateTime)}}
	}
	return field
}

func optionSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": semanticID.String()},
			"label":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxLabelBytes},
			"description": map[string]any{"type": "string", "maxLength": MaxDescriptionBytes},
			"value":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxValueBytes},
		},
		"required": []string{"id", "label", "value"},
	}
}

func freeformBoundsConstraint() map[string]any {
	return map[string]any{
		"if": map[string]any{
			"properties": map[string]any{"allow_freeform": map[string]any{"const": true}},
			"required":   []string{"allow_freeform"},
		},
		"then": map[string]any{"required": []string{"max_length"}},
		"else": map[string]any{
			"not": map[string]any{"anyOf": []any{
				map[string]any{"required": []string{"min_length"}},
				map[string]any{"required": []string{"max_length"}},
			}},
		},
	}
}

func requiredMinimumWhenRequired(property string, minimum int) map[string]any {
	return map[string]any{
		"if":   map[string]any{"properties": map[string]any{"required": map[string]any{"const": true}}},
		"then": map[string]any{"properties": map[string]any{property: map[string]any{"minimum": minimum}}},
	}
}

func appendConstraint(existing any, constraint map[string]any) []any {
	constraints, _ := existing.([]any)
	return append(constraints, constraint)
}
