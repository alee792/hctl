package interaction

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// TextInstructions returns the transport-neutral answer grammar for request.
// Adapters may style this text but must use ParseTextAnswer for interpretation.
func TextInstructions(request Request) (string, error) {
	if err := ValidateRequest(request); err != nil {
		return "", err
	}
	var lines []string
	if request.Kind == KindForm {
		lines = append(lines, "Reply with one keyed line per field:")
		for _, field := range request.Fields {
			lines = append(lines, fmt.Sprintf("- %s: %s", field.ID, fieldTextGrammar(field)))
		}
	} else {
		lines = append(lines, "Reply with "+fieldTextGrammar(*request.Field)+".")
	}
	if request.Policy.Cancellation == CancellationAllowed {
		lines = append(lines, "Reply with exactly `cancel` to cancel.")
	}
	return strings.Join(lines, "\n"), nil
}

// TextFallback combines the authored introduction with hctl's deterministic
// grammar. The authored fallback never defines parsing behavior.
func TextFallback(request Request) (string, error) {
	instructions, err := TextInstructions(request)
	if err != nil {
		return "", err
	}
	if request.FallbackText == "" {
		return "", errors.New("interactive request has no text fallback")
	}
	return request.FallbackText + "\n\n" + instructions, nil
}

func fieldTextGrammar(field Field) string {
	switch field.Kind {
	case KindConfirm:
		return "exactly `yes` or `no`"
	case KindChooseOne:
		grammar := "one option number (" + numberedOptions(field.Options) + ")"
		if field.AllowFreeform {
			grammar += " or `other=TEXT`"
		}
		return grammar
	case KindChooseMany:
		grammar := "comma-separated option numbers (" + numberedOptions(field.Options) + ")"
		if field.AllowFreeform {
			grammar += ", optionally followed by `;other=TEXT` (or `other=TEXT` alone)"
		}
		return grammar
	case KindText:
		return "the text value"
	case KindDateTime:
		switch field.DateTimeRepresentation {
		case DateOnly:
			return "a date in `YYYY-MM-DD` format"
		case TimeOnly:
			return "a time in 24-hour `HH:MM` format"
		default:
			return "an RFC 3339 date/time with an explicit offset"
		}
	default:
		return "a value"
	}
}

func numberedOptions(options []Option) string {
	parts := make([]string, len(options))
	for i, option := range options {
		parts[i] = fmt.Sprintf("%d=%s", i+1, option.Label)
	}
	return strings.Join(parts, ", ")
}

// ParseTextAnswer parses only hctl's fixed answer grammar and then delegates
// all semantic validation and normalization to NormalizeAnswer.
func ParseTextAnswer(request Request, reply string) (Answer, error) {
	if err := ValidateRequest(request); err != nil {
		return Answer{}, err
	}
	reply = strings.ReplaceAll(strings.ReplaceAll(reply, "\r\n", "\n"), "\r", "\n")
	if reply == "cancel" {
		return NormalizeAnswer(request, Answer{SchemaVersion: SchemaVersion, Action: ActionCancel})
	}
	answer := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit}
	if request.Kind != KindForm {
		field, err := parseTextField(*request.Field, reply)
		if err != nil {
			return Answer{}, err
		}
		answer.Fields = []FieldAnswer{field}
		return NormalizeAnswer(request, answer)
	}

	byID := make(map[string]Field, len(request.Fields))
	for _, field := range request.Fields {
		byID[field.ID] = field
	}
	seen := make(map[string]bool, len(request.Fields))
	for _, line := range strings.Split(reply, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" || seen[key] {
			return Answer{}, errors.New("form reply must contain unique keyed lines")
		}
		field, exists := byID[key]
		if !exists {
			return Answer{}, errors.New("form reply contains an unknown field")
		}
		parsed, err := parseTextField(field, strings.TrimSpace(value))
		if err != nil {
			return Answer{}, err
		}
		seen[key] = true
		answer.Fields = append(answer.Fields, parsed)
	}
	return NormalizeAnswer(request, answer)
}

func parseTextField(field Field, value string) (FieldAnswer, error) {
	answer := FieldAnswer{FieldID: field.ID}
	switch field.Kind {
	case KindConfirm:
		switch value {
		case "yes":
			confirmed := true
			answer.Confirmed = &confirmed
		case "no":
			confirmed := false
			answer.Confirmed = &confirmed
		default:
			return FieldAnswer{}, errors.New("confirmation reply must be exactly yes or no")
		}
	case KindChooseOne, KindChooseMany:
		choiceValue, freeform := value, ""
		freeformPresent := false
		if field.AllowFreeform {
			if strings.HasPrefix(choiceValue, "other=") {
				freeformPresent = true
				freeform = strings.TrimPrefix(choiceValue, "other=")
				choiceValue = ""
			} else if before, after, found := strings.Cut(choiceValue, ";other="); found {
				if field.Kind != KindChooseMany {
					return FieldAnswer{}, errors.New("choose-one reply has invalid freeform syntax")
				}
				freeformPresent = true
				choiceValue, freeform = before, after
			}
		}
		var ordinals []int
		var err error
		if choiceValue != "" {
			ordinals, err = parseOrdinals(choiceValue, field.Kind == KindChooseMany, len(field.Options))
		} else if !freeformPresent {
			err = errors.New("choice reply is empty")
		}
		if err != nil {
			return FieldAnswer{}, err
		}
		for _, ordinal := range ordinals {
			answer.OptionIDs = append(answer.OptionIDs, field.Options[ordinal-1].ID)
		}
		if freeformPresent {
			answer.Freeform = &freeform
		}
	case KindText:
		text := value
		answer.Text = &text
	case KindDateTime:
		dateTime := value
		answer.DateTime = &dateTime
	default:
		return FieldAnswer{}, errors.New("unsupported text answer field")
	}
	return answer, nil
}

func parseOrdinals(value string, many bool, optionCount int) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("choice reply is empty")
	}
	parts := []string{value}
	if many {
		parts = strings.Split(value, ",")
	} else if strings.Contains(value, ",") {
		return nil, errors.New("choose-one reply must contain one option number")
	}
	seen := make(map[int]bool, len(parts))
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		ordinal, err := strconv.Atoi(part)
		if err != nil || strconv.Itoa(ordinal) != part || ordinal < 1 || ordinal > optionCount || seen[ordinal] {
			return nil, errors.New("choice reply contains an invalid option number")
		}
		seen[ordinal] = true
		result = append(result, ordinal)
	}
	return result, nil
}
