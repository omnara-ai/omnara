package textutil

import "testing"

func TestIsLowerURLSafeLabel(t *testing.T) {
	tests := map[string]bool{
		"github":   true,
		"corp-sso": true,
		"g1":       true,
		"":         false,
		"-bad":     false,
		"bad-":     false,
		"Bad":      false,
		"bad_slug": false,
		"bad.slug": false,
		"bad/slug": false,
	}
	for value, want := range tests {
		if got := IsLowerURLSafeLabel(value); got != want {
			t.Fatalf("IsLowerURLSafeLabel(%q)=%v want %v", value, got, want)
		}
	}
}
