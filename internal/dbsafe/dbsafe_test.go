package dbsafe

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "valid", value: "hello"},
		{name: "NUL", value: "before\x00after", wantErr: true},
		{name: "invalid UTF-8", value: string([]byte{0xff}), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Text(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("Text(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}

func TestJSONStrings(t *testing.T) {
	largeNumber := strings.Repeat("9", 400)
	for _, test := range []struct {
		name    string
		value   []byte
		wantErr bool
	}{
		{name: "large number", value: []byte(`{"value":` + largeNumber + `}`)},
		{name: "invalid UTF-8 is decoded", value: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
		{name: "NUL value", value: []byte(`{"value":"before\u0000after"}`), wantErr: true},
		{name: "NUL key", value: []byte(`{"before\u0000after":true}`), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := JSONStrings(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("JSONStrings(%s) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}

func TestJSONBRejectsNumbersOutsidePostgreSQLNumericRange(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		value   []byte
		wantErr bool
	}{
		{name: "largest power", value: []byte(`{"value":1e131071}`)},
		{name: "shifted largest power", value: []byte(`{"value":0.1e131072}`)},
		{name: "smallest power", value: []byte(`{"value":1e-16383}`)},
		{name: "zero with largest parsed exponent", value: []byte(`{"value":0e1073741823}`)},
		{name: "integer overflow", value: []byte(`{"value":1e131072}`), wantErr: true},
		{name: "fraction overflow", value: []byte(`{"value":1e-16384}`), wantErr: true},
		{name: "input scale overflow", value: []byte(`{"value":1.0e-16383}`), wantErr: true},
		{name: "huge exponent", value: []byte(`{"value":1e1000000}`), wantErr: true},
		{name: "parsed exponent overflow", value: []byte(`{"value":0e1073741824}`), wantErr: true},
		{
			name:    "oversized integer",
			value:   append(append([]byte(`{"value":`), bytes.Repeat([]byte{'9'}, 131_073)...), '}'),
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := JSONB(test.value, 256*1024)
			if (err != nil) != test.wantErr {
				t.Fatalf("JSONB() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestJSONBBoundsProjectedPostgreSQLText(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		value   []byte
		wantErr bool
	}{
		{name: "ordinary exponent", value: []byte(`{"value":1e1000}`)},
		{name: "single largest exponent", value: []byte(`{"value":1e131071}`)},
		{
			name:    "two largest exponents exceed aggregate budget",
			value:   []byte(`{"a":1e131071,"b":1e131071}`),
			wantErr: true,
		},
		{
			name:    "PostgreSQL structural spaces exceed aggregate budget",
			value:   compactObjectWithZeroValues(t, 23_000),
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := JSONB(test.value, 256*1024)
			if (err != nil) != test.wantErr {
				t.Fatalf("JSONB() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func compactObjectWithZeroValues(t *testing.T, count int) []byte {
	t.Helper()

	var value bytes.Buffer
	value.WriteByte('{')
	for index := range count {
		if index > 0 {
			value.WriteByte(',')
		}
		fmt.Fprintf(&value, `"k%05d":0`, index)
	}
	value.WriteByte('}')
	return value.Bytes()
}
