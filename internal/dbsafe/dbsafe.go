package dbsafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	postgresNumericMaxFractionalDigits = int64(16_383)
	postgresNumericMaxIntegerDigits    = int64(131_072)
	postgresNumericMaxParsedExponent   = int64(1_073_741_823)
)

func Text(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("contains invalid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("contains U+0000")
	}
	return nil
}

func JSONStrings(raw []byte) error {
	return validateJSONTokens(raw, nil)
}

// JSONB validates the parts of a JSON value that PostgreSQL's jsonb type
// accepts more narrowly than Go's JSON decoder: strings cannot contain U+0000,
// numbers must fit PostgreSQL's unconstrained numeric domain, and the
// conservatively projected jsonb::text representation must fit maxTextBytes.
// The projection prevents compact exponent-form numbers from amplifying into
// an unbounded allocation before a database CHECK can reject them.
func JSONB(raw []byte, maxTextBytes int) error {
	if maxTextBytes <= 0 {
		return errors.New("PostgreSQL JSON text budget must be positive")
	}
	projectedBytes := int64(len(raw) + jsonbStructuralSpaceCount(raw))
	validateNumber := func(number json.Number) error {
		numberBytes, err := postgresNumeric(number)
		if err != nil {
			return err
		}
		if lexicalBytes := int64(len(number)); numberBytes > lexicalBytes {
			projectedBytes += numberBytes - lexicalBytes
		}
		if projectedBytes > int64(maxTextBytes) {
			return fmt.Errorf("PostgreSQL JSON text exceeds the %d-byte limit", maxTextBytes)
		}
		return nil
	}
	if projectedBytes > int64(maxTextBytes) {
		return fmt.Errorf("PostgreSQL JSON text exceeds the %d-byte limit", maxTextBytes)
	}
	return validateJSONTokens(raw, validateNumber)
}

func validateJSONTokens(raw []byte, validateNumber func(json.Number) error) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode JSON: %w", err)
		}
		if value, ok := token.(string); ok {
			if err := Text(value); err != nil {
				return err
			}
		}
		if validateNumber != nil {
			if value, ok := token.(json.Number); ok {
				if err := validateNumber(value); err != nil {
					return err
				}
			}
		}
	}
}

func postgresNumeric(number json.Number) (int64, error) {
	original := string(number)
	negative := strings.HasPrefix(original, "-")
	text := strings.TrimPrefix(original, "-")
	mantissa := text
	exponent := int64(0)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		mantissa = text[:index]
		parsed, err := strconv.ParseInt(text[index+1:], 10, 64)
		if err != nil {
			return 0, errors.New("JSON number exceeds PostgreSQL numeric range")
		}
		if parsed > postgresNumericMaxParsedExponent || parsed < -postgresNumericMaxParsedExponent {
			return 0, errors.New("JSON number exceeds PostgreSQL numeric range")
		}
		exponent = parsed
	}
	integerDigits := len(mantissa)
	fractionalDigits := 0
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		integerDigits = index
		fractionalDigits = len(mantissa) - index - 1
	}
	scale := int64(fractionalDigits) - exponent
	if scale > postgresNumericMaxFractionalDigits {
		return 0, errors.New("JSON number exceeds PostgreSQL numeric range")
	}
	firstNonzero := -1
	digitIndex := 0
	for _, character := range mantissa {
		if character == '.' {
			continue
		}
		if firstNonzero < 0 && character != '0' {
			firstNonzero = digitIndex
		}
		digitIndex++
	}
	if firstNonzero < 0 {
		if scale > 0 {
			return 2 + scale, nil
		}
		return 1, nil
	}
	maximumExponent := postgresNumericMaxIntegerDigits - int64(integerDigits) + int64(firstNonzero)
	if exponent > maximumExponent {
		return 0, errors.New("JSON number exceeds PostgreSQL numeric range")
	}
	integerOutputDigits := int64(integerDigits) + exponent
	if integerOutputDigits < 1 {
		integerOutputDigits = 1
	}
	outputBytes := integerOutputDigits
	if negative {
		outputBytes++
	}
	if scale > 0 {
		outputBytes += 1 + scale
	}
	return outputBytes, nil
}

// PostgreSQL's jsonb text form inserts one space after each comma and colon.
// Counting separators outside strings is conservative even for noncanonical
// input that already contains whitespace.
func jsonbStructuralSpaceCount(raw []byte) int {
	spaces := 0
	inString := false
	escaped := false
	for _, character := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch character {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case ',', ':':
			spaces++
		}
	}
	return spaces
}
