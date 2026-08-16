package resourcename

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "empty optional name", value: ""},
		{name: "ordinary interior spaces", value: "Studio  54"},
		{name: "international", value: "研究開発 شركة برمجيات"},
		{name: "emoji", value: "🚀 Lab"},
		{name: "ordinary punctuation", value: "R&D (West)"},
		{name: "punctuation only", value: ".!?"},
		{name: "decomposed Unicode preserved", value: "Cafe\u0301"},
		{name: "at code point limit", value: strings.Repeat("界", MaxCodePoints)},
		{
			name:    "above code point limit",
			value:   strings.Repeat("界", MaxCodePoints+1),
			wantErr: "cannot exceed 64 Unicode characters",
		},
		{name: "leading space", value: " Acme", wantErr: "must not start or end with whitespace"},
		{name: "trailing space", value: "Acme ", wantErr: "must not start or end with whitespace"},
		{name: "leading non-ASCII space", value: "\u00a0Acme", wantErr: "must not start or end with whitespace"},
		{name: "trailing non-ASCII space", value: "Acme\u00a0", wantErr: "must not start or end with whitespace"},
		{name: "interior non-ASCII space", value: "Acme\u00a0Labs", wantErr: "may only use ordinary spaces"},
		{name: "tab", value: "Acme\tLabs", wantErr: "unsupported control or format character"},
		{name: "newline", value: "Acme\nLabs", wantErr: "unsupported control or format character"},
		{name: "NUL", value: "Acme\x00Labs", wantErr: "unsupported control or format character"},
		{name: "zero width joiner", value: "Acme\u200dLabs", wantErr: "unsupported control or format character"},
		{name: "bidi override", value: "Acme\u202eLabs", wantErr: "unsupported control or format character"},
		{name: "invalid UTF-8", value: string([]byte{0xff}), wantErr: "must be valid UTF-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate("name", tt.value)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(%q): %v", tt.value, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate(%q) error = %v, want containing %q", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestValidateWithMaxUsesMatchingUTF8ByteCeiling(t *testing.T) {
	const maxCodePoints = 2
	if err := ValidateWithMax("name", "😀😀", maxCodePoints); err != nil {
		t.Fatalf("ValidateWithMax at UTF-8 byte ceiling: %v", err)
	}
}
