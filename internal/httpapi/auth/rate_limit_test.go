package auth

import (
	"net"
	"net/http"
	"testing"
)

func TestClientBucketUsesForwardedHeadersOnlyFromTrustedProxy(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/api/auth/login", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.RemoteAddr = "10.0.0.12:4321"
	req.Host = "api.example.com"
	req.Header.Set("X-Forwarded-For", "198.51.100.44, 10.0.0.12")
	req.Header.Set("X-Real-IP", "198.51.100.45")

	if got := (&Handler{}).clientBucket(req); got != "10.0.0.12" {
		t.Fatalf("untrusted proxy client bucket=%q, want remote address", got)
	}
	_, trustedProxy, err := net.ParseCIDR("10.0.0.0/24")
	if err != nil {
		t.Fatalf("parse CIDR: %v", err)
	}
	handler := &Handler{trustedProxyNets: []*net.IPNet{trustedProxy}}
	if got := handler.clientBucket(req); got != "198.51.100.44" {
		t.Fatalf("trusted proxy client bucket=%q, want forwarded client", got)
	}

	req.Header.Set("X-Forwarded-For", "198.51.100.44, 203.0.113.9, 10.0.0.12")
	if got := handler.clientBucket(req); got != "203.0.113.9" {
		t.Fatalf("trusted proxy client bucket=%q, want rightmost untrusted forwarded client", got)
	}

	req.Header.Del("X-Forwarded-For")
	if got := handler.clientBucket(req); got != "198.51.100.45" {
		t.Fatalf("trusted proxy x-real-ip bucket=%q, want real ip", got)
	}
}
