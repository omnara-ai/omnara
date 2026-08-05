package httpapi

import (
	"strings"
	"testing"
)

func TestConfiguredOriginMatchesCanonicalHosts(t *testing.T) {
	origin, err := parseConfiguredOrigin(" https://Example.COM./ ")
	if err != nil {
		t.Fatalf("parse configured origin: %v", err)
	}
	for _, host := range []string{"example.com", "example.com:443", "EXAMPLE.COM."} {
		if !origin.matchesHost(host) {
			t.Fatalf("expected configured origin to match host %q", host)
		}
	}
	for _, host := range []string{"", "example.com:444", "other.example.com"} {
		if origin.matchesHost(host) {
			t.Fatalf("expected configured origin not to match host %q", host)
		}
	}
}

func TestConfiguredOriginMatchesIPv6Hosts(t *testing.T) {
	origin, err := parseConfiguredOrigin("https://[2001:db8::1]:8443")
	if err != nil {
		t.Fatalf("parse configured origin: %v", err)
	}
	for _, host := range []string{"[2001:db8::1]:8443", "[2001:DB8::1]:8443"} {
		if !origin.matchesHost(host) {
			t.Fatalf("expected configured origin to match host %q", host)
		}
	}
	for _, host := range []string{"[2001:db8::1]", "[2001:db8::1]:443", "[2001:db8::2]:8443"} {
		if origin.matchesHost(host) {
			t.Fatalf("expected configured origin not to match host %q", host)
		}
	}
}

func TestIsLocalOnlyHost(t *testing.T) {
	for _, host := range []string{
		"localhost",
		"localhost:5173",
		"127.0.0.1:8080",
		"[::1]:8080",
		"Localhost.",
		"host.docker.internal",
		"host.docker.internal:8080",
	} {
		if !isLocalOnlyHost(host) {
			t.Fatalf("expected %q to be a local-only host", host)
		}
	}
	for _, host := range []string{
		"",
		"example.com",
		"example.com:8080",
		"10.0.0.1:8080",
		"localhost.example.com",
		"evil-host.docker.internal.example.com",
	} {
		if isLocalOnlyHost(host) {
			t.Fatalf("expected %q not to be a local-only host", host)
		}
	}
}

func TestConfiguredOriginRejectsMalformedPublicURL(t *testing.T) {
	_, err := New(discardLogger(), nil, WithPublicURL("not-a-url"))
	if err == nil || !strings.Contains(err.Error(), "invalid public URL") {
		t.Fatalf("New error = %v, want invalid public URL", err)
	}
}
