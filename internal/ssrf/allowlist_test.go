package ssrf

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseAllowlistEntries(t *testing.T) {
	t.Parallel()
	list, err := ParseEndpointAllowlist(
		[]string{"10.0.5.21", "10.1.2.3/16", "10.0.5.21:8000", "10.0.0.0/8:8000", "[fd00::5]:8080", "fd00::/8"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !list.PermitsIP(net.ParseIP("10.1.200.1"), false) {
		t.Fatal("unmasked CIDR entry was not masked")
	}
	invalid := []string{
		"localhost:11434", "10.0.0.0/33", "10.0.5.21:0", "10.0.5.21:http", "[::1]", "fe80::1%eth0", "::ffff:10.0.5.21",
	}
	for _, raw := range invalid {
		if _, err := ParseEndpointAllowlist([]string{raw}); err == nil {
			t.Errorf("ParseEndpointAllowlist(%q) succeeded, want a parse error", raw)
		}
	}
}

func TestAllowlistPermits(t *testing.T) {
	t.Parallel()
	endpoints, err := ParseEndpointAllowlist([]string{"10.0.5.0/24", "192.168.1.10:8000", "169.254.0.0/16", "::/0"})
	if err != nil {
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
			t.Parallel()
			if got := endpoints.permits(net.ParseIP(test.ip), test.port, false, test.plainHTTP); got != test.want {
				t.Fatalf("permits(%s, %d, plainHTTP=%v) = %v, want %v", test.ip, test.port, test.plainHTTP, got, test.want)
			}
		})
	}

	if endpoints.permits(net.ParseIP("93.184.216.34"), 80, true, true) ||
		!endpoints.permits(net.ParseIP("127.0.0.1"), 80, true, true) {
		t.Fatal("dev mode should allow plain HTTP to loopback only")
	}
	if (*Allowlist)(nil).permits(net.ParseIP("10.0.5.21"), 443, false, false) {
		t.Fatal("no list reached a private address")
	}
	if IsAllowedIP(net.ParseIP("10.0.5.21"), false) {
		t.Fatal("IsAllowedIP reached a listed range")
	}
	webFetch, err := ParseWebFetchAllowlist([]string{"10.0.3.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for rawURL, want := range map[string]bool{
		"http://10.0.5.21/":         true,
		"http://10.0.6.1/":          false,
		"http://192.168.1.10:8000/": true,
		"http://192.168.1.10/":      false,
		"http://ollama/":            true,
	} {
		if got := endpoints.PermitsPlainHTTP(mustParseURL(t, rawURL)); got != want {
			t.Errorf("PermitsPlainHTTP(%s) = %v, want %v", rawURL, got, want)
		}
	}
	if webFetch.PermitsPlainHTTP(mustParseURL(t, "http://ollama/")) {
		t.Fatal("web_fetch kept plain HTTP for a hostname")
	}

	endpointsCatchAll, err := ParseEndpointAllowlist([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatal(err)
	}
	webFetchCatchAll, err := ParseWebFetchAllowlist([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"169.254.170.2", "169.254.170.23", "100.100.100.200", "168.63.129.16", "fd00:ec2::23", "fd20:ce::254",
		"fd00:c1::a9fe:a9fe", "fe80::a9fe:a9fe", "64:ff9b::169.254.170.2", "64:ff9b::a83f:8110",
	} {
		ip := net.ParseIP(raw)
		for _, list := range []*Allowlist{endpointsCatchAll, webFetchCatchAll} {
			for _, devMode := range []bool{false, true} {
				if list.PermitsIP(ip, devMode) || list.permits(ip, 443, devMode, false) ||
					list.permits(ip, 80, devMode, true) {
					t.Errorf("catch-all list reached metadata address %s (dev mode %v)", raw, devMode)
				}
			}
		}
	}
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestTransportEnforcesAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := ParseEndpointAllowlist([]string{"127.0.0.1:" + port})
	if err != nil {
		t.Fatal(err)
	}
	webFetch, err := ParseWebFetchAllowlist([]string{"10.0.3.0/24"})
	if err != nil {
		t.Fatal(err)
	}

	get := func(allowlist *Allowlist, target string) error {
		response, err := (&http.Client{Transport: NewTransport(TransportOptions{Allowlist: allowlist})}).Get(target)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}
	if err := get(endpoints, server.URL); err != nil {
		t.Fatalf("listed address: %v", err)
	}
	err = get(webFetch, server.URL)
	if !errors.Is(err, ErrBlockedAddress) || !strings.Contains(err.Error(), "needs an allowed private address") {
		t.Fatalf("other list error = %v, want a blocked error saying plain HTTP needs an allowed address", err)
	}
	if err := get(nil, server.URL); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("no list error = %v, want ErrBlockedAddress", err)
	}

	listedPort, err := ParseWebFetchAllowlist([]string{"0.0.0.0/0:11434"})
	if err != nil {
		t.Fatal(err)
	}
	if err := get(listedPort, "http://93.184.216.34/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("plain HTTP to an unlisted public port error = %v, want ErrBlockedAddress", err)
	}
	devTransport := NewTransport(TransportOptions{Allowlist: listedPort, AllowAnyPlainHTTP: true})
	if _, marked := devTransport.(plainHTTPMarker); marked {
		t.Fatal("AllowAnyPlainHTTP still restricts plain HTTP")
	}
}

func TestPlainHTTPMarkerReachesDial(t *testing.T) {
	var plainHTTP []bool
	marker := plainHTTPMarker{Transport: &http.Transport{
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
