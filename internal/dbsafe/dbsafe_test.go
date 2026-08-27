package dbsafe

import (
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
