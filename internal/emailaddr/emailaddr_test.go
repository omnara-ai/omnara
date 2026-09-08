package emailaddr

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/bidi"
	"golang.org/x/text/unicode/norm"
)

func TestReviewedUnicodeVersions(t *testing.T) {
	t.Parallel()
	for source, version := range map[string]string{
		"Go":            unicode.Version,
		"IDNA":          idna.UnicodeVersion,
		"normalization": norm.Version,
		"bidi":          bidi.UnicodeVersion,
	} {
		if version != "17.0.0" {
			t.Errorf("%s uses Unicode %s; review email-key compatibility before updating this test", source, version)
		}
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "ascii", email: " User+Tag@Example.COM ", want: "user+tag@example.com"},
		{name: "unicode domain", email: "User@BÜCHER.example", want: "user@xn--bcher-kva.example"},
		{name: "decomposed domain", email: "User@BU\u0308CHER.example", want: "user@xn--bcher-kva.example"},
		{name: "punycode domain", email: "User@XN--BCHER-KVA.example", want: "user@xn--bcher-kva.example"},
		{name: "ideographic dot", email: "User@Example。COM", want: "user@example.com"},
		{name: "sharp s is not transitional", email: "user@straße.de", want: "user@xn--strae-oqa.de"},
		{name: "capital sharp s maps per UTS 46", email: "user@STRAẞE.de", want: "user@xn--strae-oqa.de"},
		{name: "dotted capital I composed", email: "user@İstanbul.com", want: "user@xn--istanbul-o0e.com"},
		{name: "dotted capital I decomposed", email: "user@I\u0307stanbul.com", want: "user@xn--istanbul-o0e.com"},
		{name: "unicode local is lowercased only", email: "Üser@Example.com", want: "üser@example.com"},
		{name: "decomposed local is not recomposed", email: "U\u0308ser@Example.com", want: "u\u0308ser@example.com"},
		{name: "Unicode 17 local casing", email: "\u1c89@Example.com", want: "\u1c8a@example.com"},
		{name: "palochka domain mapping", email: "User@\u04c0.example", want: "user@xn--s5a.example"},
		{name: "ignored domain filler", email: "User@ex\u3164ample.com", want: "user@example.com"},
		{name: "invalid hostname keeps legacy key", email: "User@exa_mple.com", want: "user@exa_mple.com"},
		{name: "empty label keeps legacy key", email: "user@a..b.com", want: "user@a..b.com"},
		{name: "bare ace prefix label keeps legacy key", email: "user@xn--.example", want: "user@xn--.example"},
		{name: "host that maps to nothing keeps legacy key", email: "user@\u00ad", want: "user@\u00ad"},
		{
			name:  "over-long label keeps legacy key",
			email: "user@" + strings.Repeat("a", 64) + ".com",
			want:  "user@" + strings.Repeat("a", 64) + ".com",
		},
		{
			name:  "invalid unicode domain keeps spelling, ascii case folded",
			email: "User@BÜCHER..Example",
			want:  "user@bÜcher..example",
		},
		{
			name:  "rooted unicode domain keeps spelling, ascii case folded",
			email: "User@BÜCHER.example.",
			want:  "user@bÜcher.example.",
		},
		{
			name:  "invalid bidi domain keeps spelling",
			email: "User@\u0628\U0001171e.example",
			want:  "user@\u0628\U0001171e.example",
		},
		{name: "space before at is preserved", email: "a b @Example.com", want: "a b @example.com"},
		{name: "space after at keeps legacy key", email: "User@ Example.com", want: "user@ example.com"},
		{name: "missing local part", email: "@Example.com", want: "@example.com"},
		{name: "missing domain", email: "User@", want: "user@"},
		{name: "not an email", email: " Not-An-Email ", want: "not-an-email"},
		{name: "empty", email: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(test.email); got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.email, got, test.want)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{"User@BÜCHER.example", "user@bücher.example"},
		{"user@bücher.example", "user@bu\u0308cher.example"},
		{"user@bücher.example", "user@xn--bcher-kva.example"},
		{"user@STRAẞE.de", "user@straße.de"},
		{"user@ex\u3164ample.com", "user@example.com"},
		{"Üser@example.com", "üser@example.com"},
		{" user@example.com ", "USER@EXAMPLE.COM"},
	}
	for _, pair := range same {
		if !Equal(pair[0], pair[1]) {
			t.Errorf("Equal(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{"user@example.com", "user@example.org"},
		{"user@example.com", "other@example.com"},
		{"user@example.com", "user@example.com."},
		{"üser@example.com", "u\u0308ser@example.com"},
		{"user@STRAẞE.de", "user@strasse.de"},
	}
	for _, pair := range different {
		if Equal(pair[0], pair[1]) {
			t.Errorf("Equal(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	t.Parallel()
	alphabet := []rune("aZ09.-_+@ üÜ\u0308ßẞİ。中文\u0301\u04c0\u1c89\u3164ex")
	r := rand.New(rand.NewPCG(7, 0))
	for range 50000 {
		runes := make([]rune, r.IntN(24))
		for j := range runes {
			runes[j] = alphabet[r.IntN(len(alphabet))]
		}
		input := string(runes)
		once := Normalize(input)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize is not idempotent for %q: %q then %q", input, once, twice)
		}
	}
}

func asciiCorpus() []string {
	const ldh = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-"
	domains := []string{
		"example.com", "EXAMPLE.COM", "example.com.", "-bad.com", "bad-.com", "ab--cd.com",
		"xn--bcher-kva.example", "XN--BCHER-KVA.example", "xn--zzzz.example", "xn--.example",
		"example.xn--", "a.xn--.b", "xn--", "a_b.com", "localhost", "1.2.3.4", "[1.2.3.4]",
		"exa mple.com", "ex!ample.com", "", ".", "a..b.com", ".example.com",
		strings.Repeat("a", 63) + ".com", strings.Repeat("a", 64) + ".com",
		strings.Repeat("a.", 130) + "com",
	}
	r := rand.New(rand.NewPCG(1, 0))
	for range 50000 {
		labels := make([]string, 1+r.IntN(4))
		for j := range labels {
			label := make([]byte, 1+r.IntN(12))
			for k := range label {
				label[k] = ldh[r.IntN(len(ldh))]
			}
			if r.IntN(10) == 0 {
				labels[j] = "xn--" + string(label)
			} else {
				labels[j] = string(label)
			}
		}
		domains = append(domains, strings.Join(labels, "."))
	}
	return domains
}

func TestNormalizeASCIIIsFixedPoint(t *testing.T) {
	t.Parallel()
	locals := []string{"User+Tag", "a b", "a ", "\"quoted@part\"", "x.y_z-9", "A"}
	for _, domain := range asciiCorpus() {
		for _, local := range locals {
			for _, input := range []string{" " + local + "@" + domain + " ", local + "@ " + domain} {
				if got, want := Normalize(input), strings.ToLower(strings.TrimSpace(input)); got != want {
					t.Fatalf("ASCII input %q changed: got %q, want %q", input, got, want)
				}
			}
		}
	}
}

func TestDomainProfileOnlyAddsFailuresToLookup(t *testing.T) {
	t.Parallel()
	inputs := append(asciiCorpus(),
		"bücher.example", "BÜCHER.example", "bu\u0308cher.example", "straße.de", "STRAẞE.de",
		"İstanbul.com", "I\u0307stanbul.com", "example。com", "\u0308example.com", "中文.中国",
		"\u04c0.example", "\u0628\U0001171e.example", "BÜCHER.example.",
		"ex\u3164ample.com",
	)
	for _, input := range inputs {
		want, lookupErr := idna.Lookup.ToASCII(input)
		got, err := domain.ToASCII(input)
		if lookupErr != nil && err == nil {
			t.Fatalf("domain profile accepted %q which idna.Lookup rejects: %v", input, lookupErr)
		}
		if err == nil && got != want {
			t.Fatalf("domain profile ToASCII(%q) = %q, idna.Lookup = %q", input, got, want)
		}
	}
}
