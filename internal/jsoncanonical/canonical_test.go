package jsoncanonical

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalPreservesValuesAcrossNumberSpellings(t *testing.T) {
	for _, group := range [][]string{
		{"0", "-0", "-0.0", "0e10"},
		{"1", "1.00", "0.1e1"},
		{"1e-7", "0.000000100"},
		{"9007199254740993", "9007199254740993.0", "9.007199254740993e15"},
		{"0.123456789012345678901", "123456789012345678901e-21"},
		{"-9223372036854775808", "-9223372036854775808.00"},
	} {
		var expected json.RawMessage
		for _, number := range group {
			raw := json.RawMessage(`{"nested":[` + number + `]}`)
			got, err := Canonical(raw)
			require.NoError(t, err, number)
			require.True(t, Equal(raw, got), "%s -> %s", raw, got)
			if expected == nil {
				expected = got
			}
			require.Equal(t, expected, got)
		}
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `1e1000000000`, `1e400`, `1e-400`, `1 2`} {
		_, err := Canonical([]byte(raw))
		require.Error(t, err, raw)
	}
}

func FuzzCanonicalPreservesFloat64Encoding(f *testing.F) {
	for _, number := range []float64{0, 1, 0.1, 1e-7, math.SmallestNonzeroFloat64, math.MaxFloat64} {
		f.Add(number)
	}
	f.Fuzz(func(t *testing.T, number float64) {
		if math.IsNaN(number) || math.IsInf(number, 0) || (number == 0 && math.Signbit(number)) {
			return
		}
		encoded, err := json.Marshal(number)
		require.NoError(t, err)
		for _, spelling := range []string{string(encoded), strconv.FormatFloat(number, 'f', -1, 64)} {
			canonical, err := Canonical([]byte(spelling))
			require.NoError(t, err)
			require.Equal(t, json.RawMessage(encoded), canonical)
		}
	})
}
