package memorystore

import (
	"strings"
	"testing"
)

func TestValidateDescription(t *testing.T) {
	for _, test := range []struct {
		name, description string
		invalid           bool
	}{
		{name: "empty"},
		{name: "ASCII limit", description: strings.Repeat("a", 1024)},
		{name: "Unicode limit", description: strings.Repeat("😀", 1024)},
		{name: "ASCII over limit", description: strings.Repeat("a", 1025), invalid: true},
		{name: "Unicode over limit", description: strings.Repeat("😀", 1025), invalid: true},
		{name: "NUL", description: "a\x00b", invalid: true},
		{name: "invalid UTF-8", description: "\xff", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDescription(test.description)
			if (err != nil) != test.invalid {
				t.Fatalf("validate description: %v, want invalid=%t", err, test.invalid)
			}
		})
	}
}
