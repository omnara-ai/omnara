package identitystore

import (
	"slices"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "ascii", email: " User+Tag@Example.COM ", want: "user+tag@example.com"},
		{name: "unicode domain", email: "User@BÜCHER.example", want: "user@xn--bcher-kva.example"},
		{name: "ascii idna domain", email: "User@XN--BCHER-KVA.example", want: "user@xn--bcher-kva.example"},
		{name: "unicode dot", email: "User@Example。COM", want: "user@example.com"},
		{name: "non email", email: " Not-An-Email ", want: "not-an-email"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeEmail(test.email); got != test.want {
				t.Fatalf("NormalizeEmail(%q) = %q, want %q", test.email, got, test.want)
			}
		})
	}
}

func TestNormalizedEmailLookupKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		email string
		want  []string
	}{
		{
			name:  "unicode domain",
			email: "User@BÜCHER.example",
			want: []string{
				"user@xn--bcher-kva.example",
				"user@bücher.example",
				"user@bu\u0308cher.example",
			},
		},
		{
			name:  "ascii idna domain",
			email: "User@XN--BCHER-KVA.example",
			want: []string{
				"user@xn--bcher-kva.example",
				"user@bücher.example",
				"user@bu\u0308cher.example",
			},
		},
		{
			name:  "legacy decomposed domain",
			email: "User@BU\u0308CHER.example",
			want: []string{
				"user@xn--bcher-kva.example",
				"user@bu\u0308cher.example",
				"user@bücher.example",
			},
		},
		{
			name:  "local part unchanged",
			email: "Üser@BÜCHER.example",
			want: []string{
				"üser@xn--bcher-kva.example",
				"üser@bücher.example",
				"üser@bu\u0308cher.example",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizedEmailLookupKeys(test.email); !slices.Equal(got, test.want) {
				t.Fatalf("normalizedEmailLookupKeys(%q) = %q, want %q", test.email, got, test.want)
			}
		})
	}
}
