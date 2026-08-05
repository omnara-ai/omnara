package httporigin

import "testing"

func TestCanonicalHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scheme string
		host   string
		want   string
	}{
		{name: "empty", scheme: "https", host: "", want: ""},
		{name: "lowercase", scheme: "https", host: "Example.COM", want: "example.com"},
		{name: "trailing dot", scheme: "https", host: "Example.COM.", want: "example.com"},
		{name: "https default port", scheme: "https", host: "Example.COM:443", want: "example.com"},
		{name: "http default port", scheme: "http", host: "Example.COM:80", want: "example.com"},
		{name: "nondefault port", scheme: "https", host: "Example.COM:8443", want: "example.com:8443"},
		{name: "bracketed ipv6", scheme: "https", host: "[2001:db8::1]", want: "2001:db8::1"},
		{name: "bracketed ipv6 default port", scheme: "https", host: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "bracketed ipv6 nondefault port", scheme: "https", host: "[2001:db8::1]:8443", want: "[2001:db8::1]:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalHost(tc.scheme, tc.host); got != tc.want {
				t.Fatalf("CanonicalHost(%q, %q) = %q, want %q", tc.scheme, tc.host, got, tc.want)
			}
		})
	}
}
