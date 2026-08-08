// Package version validates and exposes the hctl build version.
package version

import (
	"errors"
	"strings"
)

// Value is replaced with an exact release version by the artifact build.
var Value = "0.1.0-dev"

func Validate(value string) error {
	_, err := parse(value)
	return err
}

// Compare compares two validated semantic versions. Build metadata does not
// affect precedence. The result is negative when left precedes right, zero
// when their precedence is equal, and positive when left follows right.
func Compare(left, right string) (int, error) {
	leftVersion, err := parse(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := parse(right)
	if err != nil {
		return 0, err
	}
	for index := range leftVersion.core {
		if compared := compareNumeric(leftVersion.core[index], rightVersion.core[index]); compared != 0 {
			return compared, nil
		}
	}
	if len(leftVersion.prerelease) == 0 && len(rightVersion.prerelease) == 0 {
		return 0, nil
	}
	if len(leftVersion.prerelease) == 0 {
		return 1, nil
	}
	if len(rightVersion.prerelease) == 0 {
		return -1, nil
	}
	limit := min(len(leftVersion.prerelease), len(rightVersion.prerelease))
	for index := range limit {
		leftIdentifier := leftVersion.prerelease[index]
		rightIdentifier := rightVersion.prerelease[index]
		leftNumeric := numericIdentifier(leftIdentifier)
		rightNumeric := numericIdentifier(rightIdentifier)
		switch {
		case leftNumeric && rightNumeric:
			if compared := compareNumeric(leftIdentifier, rightIdentifier); compared != 0 {
				return compared, nil
			}
		case leftNumeric:
			return -1, nil
		case rightNumeric:
			return 1, nil
		case leftIdentifier < rightIdentifier:
			return -1, nil
		case leftIdentifier > rightIdentifier:
			return 1, nil
		}
	}
	switch {
	case len(leftVersion.prerelease) < len(rightVersion.prerelease):
		return -1, nil
	case len(leftVersion.prerelease) > len(rightVersion.prerelease):
		return 1, nil
	default:
		return 0, nil
	}
}

type semanticVersion struct {
	core       [3]string
	prerelease []string
}

func parse(value string) (semanticVersion, error) {
	coreAndPre, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validIdentifiers(build, false) {
		return semanticVersion{}, errors.New("version build metadata is invalid")
	}
	core, pre, hasPre := strings.Cut(coreAndPre, "-")
	if hasPre && !validIdentifiers(pre, true) {
		return semanticVersion{}, errors.New("version prerelease is invalid")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, errors.New("version core must contain major, minor, and patch numbers")
	}
	for _, part := range parts {
		if !numericIdentifier(part) {
			return semanticVersion{}, errors.New("version core numbers must be decimal without leading zeroes")
		}
	}
	parsed := semanticVersion{core: [3]string{parts[0], parts[1], parts[2]}}
	if hasPre {
		parsed.prerelease = strings.Split(pre, ".")
	}
	return parsed, nil
}

func compareNumeric(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func validIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if character < '0' || character > '9' {
				numeric = false
			}
			if !identifierCharacter(character) {
				return false
			}
		}
		if rejectNumericLeadingZero && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func identifierCharacter(character rune) bool {
	return character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '-'
}

func numericIdentifier(value string) bool {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
