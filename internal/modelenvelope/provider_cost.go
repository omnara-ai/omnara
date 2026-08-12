package modelenvelope

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

const (
	providerReportedCostUSDMaxIntegralDigits   = 15
	providerReportedCostUSDMaxFractionalDigits = 15
)

// ProviderReportedCostUSD is a canonical, non-negative decimal amount reported
// by the model provider and normalized to the database's fractional precision.
// The empty value means that the provider did not report a usable cost.
type ProviderReportedCostUSD string

// SumProviderReportedCostUSD adds raw provider decimal amounts exactly and
// normalizes only the total to the database's fixed precision.
func SumProviderReportedCostUSD(
	rawCosts ...string,
) (ProviderReportedCostUSD, bool) {
	if len(rawCosts) == 0 {
		return "", false
	}
	total := new(big.Rat)
	for _, rawCost := range rawCosts {
		// big.Rat accepts non-JSON forms such as fractions and signed values;
		// the provider-cost parser is the grammar and range authority.
		if _, ok := ParseProviderReportedCostUSD(rawCost); !ok {
			return "", false
		}
		value, ok := new(big.Rat).SetString(rawCost)
		if !ok {
			return "", false
		}
		total.Add(total, value)
	}
	return ParseProviderReportedCostUSD(
		total.FloatString(providerReportedCostUSDMaxFractionalDigits),
	)
}

func ParseProviderReportedCostUSD(raw string) (ProviderReportedCostUSD, bool) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", false
	}
	negative := raw[0] == '-'
	if negative {
		raw = raw[1:]
	}

	mantissa := raw
	exponent := int64(0)
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		if strings.ContainsAny(raw[index+1:], "eE") {
			return "", false
		}
		mantissa = raw[:index]
		parsed, err := strconv.ParseInt(raw[index+1:], 10, 32)
		if err != nil {
			return "", false
		}
		exponent = parsed
	}

	integer := mantissa
	fraction := ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		if strings.IndexByte(mantissa[index+1:], '.') >= 0 {
			return "", false
		}
		integer = mantissa[:index]
		fraction = mantissa[index+1:]
		if fraction == "" {
			return "", false
		}
	}
	if !validJSONIntegerPart(integer) || !allDecimalDigits(fraction) {
		return "", false
	}

	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return ProviderReportedCostUSD("0"), true
	}
	if negative {
		return "", false
	}
	scale := int64(len(fraction)) - exponent
	digits, scale = trimProviderCostTrailingZeros(digits, scale)
	if scale > providerReportedCostUSDMaxFractionalDigits {
		digits = roundProviderCostDigits(digits, scale)
		scale = providerReportedCostUSDMaxFractionalDigits
		if digits == "" {
			return ProviderReportedCostUSD("0"), true
		}
		digits, scale = trimProviderCostTrailingZeros(digits, scale)
	}
	if int64(len(digits))-scale > providerReportedCostUSDMaxIntegralDigits {
		return "", false
	}

	var canonical string
	switch {
	case scale <= 0:
		canonical = digits + strings.Repeat("0", int(-scale))
	case scale >= int64(len(digits)):
		canonical = "0." + strings.Repeat("0", int(scale)-len(digits)) + digits
	default:
		point := len(digits) - int(scale)
		canonical = digits[:point] + "." + digits[point:]
	}
	return ProviderReportedCostUSD(canonical), true
}

// roundProviderCostDigits rounds a positive decimal to the configured scale.
// Ties round up, matching PostgreSQL numeric typmod coercion for positive values.
// An empty result represents a value that rounded to zero.
func roundProviderCostDigits(digits string, scale int64) string {
	discard := scale - providerReportedCostUSDMaxFractionalDigits
	kept := int64(len(digits)) - discard
	if kept <= 0 {
		if kept == 0 && digits[0] >= '5' {
			return "1"
		}
		return ""
	}

	cut := int(kept)
	rounded := []byte(digits[:cut])
	if digits[cut] < '5' {
		return string(rounded)
	}
	for index := len(rounded) - 1; index >= 0; index-- {
		if rounded[index] < '9' {
			rounded[index]++
			return string(rounded)
		}
		rounded[index] = '0'
	}
	return "1" + string(rounded)
}

func trimProviderCostTrailingZeros(digits string, scale int64) (string, int64) {
	trimmed := strings.TrimRight(digits, "0")
	return trimmed, scale - int64(len(digits)-len(trimmed))
}

func ValidateProviderReportedCostUSD(cost ProviderReportedCostUSD) error {
	if cost == "" {
		return nil
	}
	canonical, ok := ParseProviderReportedCostUSD(string(cost))
	if !ok || canonical != cost {
		return errors.New("must be a canonical non-negative decimal USD amount")
	}
	return nil
}

func validJSONIntegerPart(value string) bool {
	if value == "0" {
		return true
	}
	return value != "" && value[0] >= '1' && value[0] <= '9' && allDecimalDigits(value[1:])
}

func allDecimalDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
