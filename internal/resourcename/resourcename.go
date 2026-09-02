package resourcename

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const MaxCodePoints = 64

func CanonicalizeRequired(field, value string) (string, error) {
	return canonicalize(field, value, true)
}

func CanonicalizeAllowEmpty(field, value string) (string, error) {
	return canonicalize(field, value, false)
}

func canonicalize(field, value string, required bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%s must be valid UTF-8", field)
	}
	value = norm.NFC.String(value)
	if required && value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if err := validateNormalized(field, value); err != nil {
		return "", err
	}
	return value, nil
}

func validateNormalized(field, value string) error {
	if utf8.RuneCountInString(value) > MaxCodePoints {
		return fmt.Errorf("%s cannot exceed %d Unicode characters", field, MaxCodePoints)
	}
	if value != "" {
		first, _ := utf8.DecodeRuneInString(value)
		last, _ := utf8.DecodeLastRuneInString(value)
		if unicode.IsSpace(first) || unicode.IsSpace(last) {
			return fmt.Errorf("%s must not start or end with whitespace", field)
		}
	}

	for _, r := range value {
		if r == '\ufffd' {
			return fmt.Errorf("%s contains the Unicode replacement character", field)
		}
		if unicode.IsControl(r) || unicode.In(
			r,
			unicode.Cf,
			unicode.Other_Default_Ignorable_Code_Point,
			unicode.Variation_Selector,
		) || r == '\u2800' {
			return fmt.Errorf("%s contains an unsupported invisible, control, or format character", field)
		}
		if unicode.IsSpace(r) && r != ' ' {
			return fmt.Errorf("%s may only use ordinary spaces", field)
		}
	}

	return nil
}
