package interaction

// RequestJSONSchema returns a fresh JSON Schema for the model-authored semantic
// request. Runtime decoding and validation remain authoritative.
func RequestJSONSchema() map[string]any {
	option := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": semanticID.String()},
			"label":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxLabelBytes},
			"description": map[string]any{"type": "string", "maxLength": MaxDescriptionBytes},
			"value":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxValueBytes},
		},
		"required": []string{"id", "label", "value"},
	}
	fieldKinds := []string{string(KindConfirm), string(KindChooseOne), string(KindChooseMany), string(KindText), string(KindDateTime)}
	field := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":                       map[string]any{"type": "string", "pattern": semanticID.String()},
			"kind":                     map[string]any{"type": "string", "enum": fieldKinds},
			"label":                    map[string]any{"type": "string", "minLength": 1, "maxLength": MaxLabelBytes},
			"description":              map[string]any{"type": "string", "maxLength": MaxDescriptionBytes},
			"required":                 map[string]any{"type": "boolean"},
			"options":                  map[string]any{"type": "array", "maxItems": MaxOptionsPerField, "items": option},
			"allow_freeform":           map[string]any{"type": "boolean"},
			"min_selections":           map[string]any{"type": "integer", "minimum": 0, "maximum": MaxSelections},
			"max_selections":           map[string]any{"type": "integer", "minimum": 0, "maximum": MaxSelections},
			"min_length":               map[string]any{"type": "integer", "minimum": 0, "maximum": MaxTextRunes},
			"max_length":               map[string]any{"type": "integer", "minimum": 0, "maximum": MaxTextRunes},
			"date_time_representation": map[string]any{"type": "string", "enum": []string{string(DateOnly), string(TimeOnly), string(DateTime)}},
		},
		"required": []string{"id", "kind", "label", "required"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "const": SchemaVersion},
			"kind":           map[string]any{"type": "string", "enum": append(fieldKinds, string(KindForm))},
			"prompt":         map[string]any{"type": "string", "minLength": 1, "maxLength": MaxPromptBytes},
			"fallback_text":  map[string]any{"type": "string", "maxLength": MaxFallbackBytes},
			"policy": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"expires_after_seconds": map[string]any{"type": "integer", "minimum": MinExpirySeconds, "maximum": MaxExpirySeconds},
					"cancellation":          map[string]any{"type": "string", "enum": []string{string(CancellationAllowed), string(CancellationForbidden)}},
				}, "required": []string{"expires_after_seconds", "cancellation"},
			},
			"field":  field,
			"fields": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxFields, "items": field},
		},
		"required": []string{"schema_version", "kind", "prompt", "policy"},
	}
}
