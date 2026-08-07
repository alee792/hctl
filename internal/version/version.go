// Package version validates and exposes the hctl build version.
package version

import (
	"errors"
	"strings"
)

// Value is replaced with an exact release version by the artifact build.
var Value = "0.9.0-dev"

func Validate(value string) error {
	coreAndPre, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validIdentifiers(build, false) {
		return errors.New("version build metadata is invalid")
	}
	core, pre, hasPre := strings.Cut(coreAndPre, "-")
	if hasPre && !validIdentifiers(pre, true) {
		return errors.New("version prerelease is invalid")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return errors.New("version core must contain major, minor, and patch numbers")
	}
	for _, part := range parts {
		if !numericIdentifier(part) {
			return errors.New("version core numbers must be decimal without leading zeroes")
		}
	}
	return nil
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
