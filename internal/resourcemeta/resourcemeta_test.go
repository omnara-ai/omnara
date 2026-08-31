package resourcemeta

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tooManyEntries := Metadata{}
	for i := range MaxEntries + 1 {
		tooManyEntries[fmt.Sprintf("key-%d", i)] = "value"
	}

	valid := []struct {
		name     string
		metadata Metadata
	}{
		{name: "nil", metadata: nil},
		{name: "empty", metadata: Metadata{}},
		{name: "string values", metadata: Metadata{"team": "support", "region": "us-east-1"}},
		{
			name: "boundary sizes",
			metadata: Metadata{
				strings.Repeat("k", MaxKeyLength): strings.Repeat("v", MaxValueLength),
			},
		},
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			if err := tt.metadata.Validate(); err != nil {
				t.Fatalf("validate %v: %v", tt.metadata, err)
			}
		})
	}

	invalid := []struct {
		name     string
		metadata Metadata
	}{
		{name: "empty key", metadata: Metadata{"": "value"}},
		{name: "oversized key", metadata: Metadata{strings.Repeat("k", MaxKeyLength+1): "v"}},
		{name: "oversized value", metadata: Metadata{"k": strings.Repeat("v", MaxValueLength+1)}},
		{name: "NUL key", metadata: Metadata{"before\x00after": "value"}},
		{name: "NUL value", metadata: Metadata{"key": "before\x00after"}},
		{name: "invalid UTF-8 key", metadata: Metadata{string([]byte{0xff}): "value"}},
		{name: "invalid UTF-8 value", metadata: Metadata{"key": string([]byte{0xff})}},
		{name: "too many entries", metadata: tooManyEntries},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			if err := tt.metadata.Validate(); err == nil {
				t.Fatalf("validate %v passed, want error", tt.metadata)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	raw, err := Metadata(nil).JSON()
	if err != nil {
		t.Fatalf("encode nil metadata: %v", err)
	}
	if string(raw) != "{}" {
		t.Fatalf("nil metadata encoded as %s, want {}", raw)
	}
	decoded, err := FromJSON(nil)
	if err != nil {
		t.Fatalf("decode empty column: %v", err)
	}
	if decoded == nil || len(decoded) != 0 {
		t.Fatalf("decode empty column = %v, want empty map", decoded)
	}
	if _, err := FromJSON([]byte(`["a"]`)); err == nil {
		t.Fatal("decoding a JSON array passed, want error")
	}
	if _, err := FromJSON([]byte(`{"count":3}`)); err == nil {
		t.Fatal("decoding a non-string value passed, want error")
	}
	decoded, err = FromJSON([]byte(`{"team":"support"}`))
	if err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if decoded["team"] != "support" {
		t.Fatalf("decode metadata = %v", decoded)
	}
}
