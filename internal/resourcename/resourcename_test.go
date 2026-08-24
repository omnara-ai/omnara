package resourcename

import (
	"strings"
	"testing"
)

func TestCanonicalizeOptional(t *testing.T) {
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
		{name: "decomposed Unicode accepted", value: "Cafe\u0301"},
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
		{name: "tab", value: "Acme\tLabs", wantErr: "unsupported invisible, control, or format character"},
		{name: "newline", value: "Acme\nLabs", wantErr: "unsupported invisible, control, or format character"},
		{name: "NUL", value: "Acme\x00Labs", wantErr: "unsupported invisible, control, or format character"},
		{name: "zero width joiner", value: "Acme\u200dLabs", wantErr: "unsupported invisible, control, or format character"},
		{name: "bidi override", value: "Acme\u202eLabs", wantErr: "unsupported invisible, control, or format character"},
		{name: "Hangul filler", value: "Acme\u3164Labs", wantErr: "unsupported invisible, control, or format character"},
		{name: "variation selector", value: "Acme\ufe0fLabs", wantErr: "unsupported invisible, control, or format character"},
		{name: "braille blank", value: "Acme\u2800Labs", wantErr: "unsupported invisible, control, or format character"},
		{name: "replacement character", value: "Acme\ufffdLabs", wantErr: "Unicode replacement character"},
		{name: "invalid UTF-8", value: string([]byte{0xff}), wantErr: "must be valid UTF-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalizeOptional("name", tt.value)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CanonicalizeOptional(%q): %v", tt.value, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CanonicalizeOptional(%q) error = %v, want containing %q", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestCanonicalizeRequiredRejectsEmpty(t *testing.T) {
	if _, err := CanonicalizeRequired("name", ""); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("CanonicalizeRequired empty error = %v", err)
	}
}

func TestCanonicalizeRequiredNormalizesBeforeValidation(t *testing.T) {
	got, err := CanonicalizeRequired("name", "Cafe\u0301")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Café" {
		t.Fatalf("CanonicalizeRequired() = %q, want NFC Café", got)
	}

	decomposedAtRawLimit := strings.Repeat("e\u0301", MaxCodePoints/2) + strings.Repeat("x", MaxCodePoints/2)
	got, err = CanonicalizeRequired("name", decomposedAtRawLimit)
	if err != nil {
		t.Fatalf("CanonicalizeRequired() must validate after NFC: %v", err)
	}
	if runeCount := len([]rune(got)); runeCount != MaxCodePoints {
		t.Fatalf("normalized code points = %d, want %d", runeCount, MaxCodePoints)
	}
}

func TestValidateCanonicalRequiredRejectsDecomposedInput(t *testing.T) {
	if err := ValidateCanonicalRequired("name", "Cafe\u0301"); err == nil ||
		!strings.Contains(err.Error(), "must use Unicode NFC normalization") {
		t.Fatalf("ValidateCanonicalRequired decomposed error = %v", err)
	}
	if err := ValidateCanonicalRequired("name", "Café"); err != nil {
		t.Fatalf("ValidateCanonicalRequired NFC: %v", err)
	}
}
