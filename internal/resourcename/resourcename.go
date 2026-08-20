package resourcename

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCodePoints = 64
	MaxUTF8Bytes  = 4 * MaxCodePoints
)

// Validate permits empty values; callers enforce requiredness.
func Validate(field, value string) error {
	return ValidateWithMax(field, value, MaxCodePoints)
}

func ValidateWithMax(field, value string, maxCodePoints int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxCodePoints {
		return fmt.Errorf("%s cannot exceed %d Unicode characters", field, maxCodePoints)
	}
	maxUTF8Bytes := 4 * maxCodePoints
	if len(value) > maxUTF8Bytes {
		return fmt.Errorf("%s cannot exceed %d UTF-8 bytes", field, maxUTF8Bytes)
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
