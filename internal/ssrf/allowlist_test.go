package ssrf

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigureParsesEntries(t *testing.T) {
	t.Cleanup(func() { _ = Configure(nil, nil) })

	if err := Configure(
		[]string{"10.0.5.21", "10.1.2.3/16", "10.0.5.21:8000", "10.0.0.0/8:8000", "[fd00::5]:8080", "fd00::/8"},
		[]string{"0.0.0.0/0"},
	); err != nil {
		t.Fatal(err)
	}
	if !Endpoints.PermitsIP(net.ParseIP("10.1.200.1"), false) {
		t.Fatal("unmasked CIDR entry was not masked")
	}
	invalid := []string{
		"localhost:11434", "10.0.0.0/33", "10.0.5.21:0", "10.0.5.21:http", "[::1]", "fe80::1%eth0", "::ffff:10.0.5.21",
	}
	for _, raw := range invalid {
		if err := Configure([]string{raw}, nil); err == nil || !strings.Contains(err.Error(), Endpoints.setting) {
			t.Errorf("Configure(%q) error = %v, want a parse error naming the setting", raw, err)
		}
	}
}

func TestAllowlistPermits(t *testing.T) {
	t.Cleanup(func() { _ = Configure(nil, nil) })
	if err := Configure([]string{"10.0.5.0/24", "192.168.1.10:8000", "169.254.0.0/16", "::/0"}, nil); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		ip        string
		port      uint16
		plainHTTP bool
		want      bool
	}{
		{name: "listed range", ip: "10.0.5.21", port: 443, want: true},
		{name: "listed range over plain http", ip: "10.0.5.21", port: 80, plainHTTP: true, want: true},
		{name: "ipv4-mapped listed address", ip: "::ffff:10.0.5.21", port: 443, want: true},
		{name: "listed port", ip: "192.168.1.10", port: 8000, plainHTTP: true, want: true},
		{name: "other port on listed address", ip: "192.168.1.10", port: 8001},
		{name: "unlisted private address", ip: "10.0.6.1", port: 443},
		{name: "metadata inside listed range", ip: "169.254.169.254", port: 80},
		{name: "ipv6 metadata inside listed range", ip: "fd00:ec2::254", port: 80},
		{name: "nat64 metadata inside listed range", ip: "64:ff9b::169.254.169.254", port: 80},
		{name: "ipv4-compatible metadata inside listed range", ip: "::169.254.169.254", port: 80},
		{name: "6to4 metadata inside listed range", ip: "2002:a9fe:a9fe::1", port: 80},
		{name: "nat64 address inside listed range", ip: "64:ff9b::10.0.5.21", port: 443, want: true},
		{name: "ipv6 loopback inside listed range", ip: "::1", port: 8080, plainHTTP: true, want: true},
		{name: "public address over https", ip: "93.184.216.34", port: 443, want: true},
		{name: "public address over plain http", ip: "93.184.216.34", port: 80, plainHTTP: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Endpoints.permits(net.ParseIP(test.ip), test.port, false, test.plainHTTP); got != test.want {
				t.Fatalf("permits(%s, %d, plainHTTP=%v) = %v, want %v", test.ip, test.port, test.plainHTTP, got, test.want)
			}
		})
	}

	if Endpoints.permits(net.ParseIP("93.184.216.34"), 80, true, true) ||
		!Endpoints.permits(net.ParseIP("127.0.0.1"), 80, true, true) {
		t.Fatal("dev mode should allow plain HTTP to loopback only")
	}
	if WebFetch.permits(net.ParseIP("10.0.5.21"), 443, false, false) {
		t.Fatal("web_fetch reached a range listed only for endpoints")
	}
	if IsAllowedIP(net.ParseIP("10.0.5.21"), false) {
		t.Fatal("IsAllowedIP reached a listed range")
	}
	if !Endpoints.PermitsPlainHTTP("10.0.5.21") || Endpoints.PermitsPlainHTTP("10.0.6.1") {
		t.Fatal("plain HTTP literal check does not follow the list")
	}
	if !Endpoints.PermitsPlainHTTP("ollama") || WebFetch.PermitsPlainHTTP("ollama") {
		t.Fatal("plain HTTP hostname check does not follow whether the list is set")
	}
}

func TestTransportEnforcesAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Configure(nil, nil) })
	if err := Configure([]string{"127.0.0.1:" + port}, []string{"10.0.3.0/24"}); err != nil {
		t.Fatal(err)
	}

	get := func(allowlist *Allowlist) error {
		response, err := (&http.Client{Transport: NewTransport(TransportOptions{Allowlist: allowlist})}).Get(server.URL)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}
	if err := get(Endpoints); err != nil {
		t.Fatalf("listed address: %v", err)
	}
	err = get(WebFetch)
	if !errors.Is(err, ErrBlockedAddress) || !strings.Contains(err.Error(), "not an allowed private address") {
		t.Fatalf("other list error = %v, want a blocked error saying the address is not allowed", err)
	}
	if err := get(nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("no list error = %v, want ErrBlockedAddress", err)
	}
}

func TestPlainHTTPMarkerReachesDial(t *testing.T) {
	var plainHTTP []bool
	marker := plainHTTPMarker{&http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			plainHTTP = append(plainHTTP, ctx.Value(plainHTTPKey{}) != nil)
			return nil, errors.New("stop")
		},
	}}
	client := &http.Client{Transport: marker}
	for _, rawURL := range []string{"http://example.test/", "https://example.test/"} {
		if response, err := client.Get(rawURL); err == nil {
			_ = response.Body.Close()
		}
	}
	if len(plainHTTP) != 2 || !plainHTTP[0] || plainHTTP[1] {
		t.Fatalf("plain HTTP flags seen at dial = %v, want [true false]", plainHTTP)
	}
}
