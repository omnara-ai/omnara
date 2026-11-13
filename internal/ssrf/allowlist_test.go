package ssrf

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestParseAllowlistEntries(t *testing.T) {
	t.Parallel()
	list, err := ParseAllowlist([]string{
		"10.0.5.21", "10.1.2.3/16", "10.0.5.21:8000", "10.0.0.0/8:8000", "[fd00::5]:8080", "fd00::/8",
		"100.64.0.0/10", "169.254.0.0/15", "0.0.0.0/0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !list.PermitsIP(net.ParseIP("10.1.200.1"), 443, false) {
		t.Fatal("unmasked CIDR entry was not masked")
	}
	invalid := []string{
		"localhost:11434", "10.0.0.0/33", "10.0.5.21:0", "10.0.5.21:http", "[::1]", "fe80::1%eth0", "::ffff:10.0.5.21",
		"169.254.169.254", "169.254.169.254:80", "169.254.0.0/16", "100.100.100.200", "fe80::/10", "fd00:ec2::254",
		"64:ff9b::a9fe:a9fe", "64:ff9b:1::/48", "64:ff9b:1:2::/64",
	}
	for _, raw := range invalid {
		if _, err := ParseAllowlist([]string{raw}); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want a parse error", raw)
		}
	}
}

func TestAllowlistPermits(t *testing.T) {
	t.Parallel()
	endpoints, err := ParseAllowlist([]string{"10.0.5.0/24", "192.168.1.10:8000", "169.254.0.0/15", "::/0"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		ip   string
		port uint16
		want bool
	}{
		{name: "listed range", ip: "10.0.5.21", port: 443, want: true},
		{name: "ipv4-mapped listed address", ip: "::ffff:10.0.5.21", port: 443, want: true},
		{name: "listed port", ip: "192.168.1.10", port: 8000, want: true},
		{name: "other port on listed address", ip: "192.168.1.10", port: 8001},
		{name: "unlisted private address", ip: "10.0.6.1", port: 443},
		{name: "metadata inside listed range", ip: "169.254.169.254", port: 80},
		{name: "ipv6 metadata inside listed range", ip: "fd00:ec2::254", port: 80},
		{name: "nat64 metadata inside listed range", ip: "64:ff9b::169.254.169.254", port: 80},
		{name: "local-use nat64 inside listed range", ip: "64:ff9b:1::6464:64c8", port: 80},
		{name: "ipv4-compatible metadata inside listed range", ip: "::169.254.169.254", port: 80},
		{name: "6to4 metadata inside listed range", ip: "2002:a9fe:a9fe::1", port: 80},
		{name: "nat64 address inside listed range", ip: "64:ff9b::10.0.5.21", port: 443, want: true},
		{name: "ipv6 loopback inside listed range", ip: "::1", port: 8080, want: true},
		{name: "invalid port", ip: "10.0.5.21", port: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := endpoints.Contains(net.ParseIP(test.ip), test.port); got != test.want {
				t.Fatalf("Contains(%s, %d) = %v, want %v", test.ip, test.port, got, test.want)
			}
		})
	}

	public := net.ParseIP("93.184.216.34")
	if !endpoints.PermitsIP(public, 443, false) || endpoints.Contains(public, 443) {
		t.Fatal("PermitsIP should allow public addresses without listing them")
	}
	if (*Allowlist)(nil).PermitsIP(net.ParseIP("10.0.5.21"), 443, false) {
		t.Fatal("no list reached a private address")
	}
	if IsAllowedIP(net.ParseIP("10.0.5.21"), false) {
		t.Fatal("IsAllowedIP reached a listed range")
	}
	if !endpoints.PermitsIP(net.ParseIP("192.168.1.10"), 8000, false) ||
		endpoints.PermitsIP(net.ParseIP("192.168.1.10"), 8001, false) {
		t.Fatal("PermitsIP ignored the listed port")
	}
	loopbackPort, err := ParseAllowlist([]string{"127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if !loopbackPort.PermitsLocalhost(8080) || loopbackPort.PermitsLocalhost(80) {
		t.Fatal("PermitsLocalhost ignored the listed loopback port")
	}

	catchAll, err := ParseAllowlist([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"169.254.170.2", "169.254.170.23", "100.100.100.200", "168.63.129.16", "fd00:ec2::23", "fd20:ce::254",
		"fd00:c1::a9fe:a9fe", "fe80::a9fe:a9fe", "64:ff9b::169.254.170.2", "64:ff9b::a83f:8110", "64:ff9b:1::1",
	} {
		ip := net.ParseIP(raw)
		for _, devMode := range []bool{false, true} {
			if catchAll.PermitsIP(ip, 443, devMode) || catchAll.Contains(ip, 80) {
				t.Errorf("catch-all list reached always-blocked address %s (dev mode %v)", raw, devMode)
			}
		}
	}
}

func TestTransportEnforcesAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := ParseAllowlist([]string{"127.0.0.1:" + port})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ParseAllowlist([]string{"10.0.3.0/24"})
	if err != nil {
		t.Fatal(err)
	}

	get := func(opts TransportOptions, target string) error {
		response, err := (&http.Client{Transport: NewTransport(opts)}).Get(target)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}
	if err := get(TransportOptions{Allowlist: listed, PlainHTTP: PlainHTTPAllowlistOnly}, server.URL); err != nil {
		t.Fatalf("plain HTTP to a listed address: %v", err)
	}
	err = get(TransportOptions{Allowlist: other, PlainHTTP: PlainHTTPAllowlistOnly}, server.URL)
	if !errors.Is(err, ErrBlockedAddress) || !strings.Contains(err.Error(), "needs an allowed private address") {
		t.Fatalf("unlisted address error = %v, want a blocked error saying plain HTTP needs an allowed address", err)
	}
	for _, policy := range []PlainHTTPPolicy{PlainHTTPUnrestricted, PlainHTTPAllowlistOnly, PlainHTTPLoopbackOnly} {
		if err := get(TransportOptions{PlainHTTP: policy}, server.URL); !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("no list with policy %d error = %v, want ErrBlockedAddress", policy, err)
		}
		if err := get(TransportOptions{AllowLoopback: true, PlainHTTP: policy}, server.URL); err != nil {
			t.Fatalf("dev mode loopback with policy %d: %v", policy, err)
		}
	}
	err = get(TransportOptions{Allowlist: listed, PlainHTTP: PlainHTTPLoopbackOnly}, server.URL)
	if !errors.Is(err, ErrBlockedAddress) || strings.Contains(err.Error(), "allowed private address") {
		t.Fatalf("HTTPS-only list error = %v, want a generic blocked error", err)
	}

	listedPort, err := ParseAllowlist([]string{"0.0.0.0/0:11434"})
	if err != nil {
		t.Fatal(err)
	}
	err = get(TransportOptions{Allowlist: listedPort, PlainHTTP: PlainHTTPAllowlistOnly}, "http://93.184.216.34/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("plain HTTP to an unlisted public port error = %v, want ErrBlockedAddress", err)
	}
	if err := get(TransportOptions{}, "http://93.184.216.34/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("default plain HTTP to a public address error = %v, want ErrBlockedAddress", err)
	}
	if _, marked := NewTransport(TransportOptions{PlainHTTP: PlainHTTPUnrestricted}).(plainHTTPMarker); marked {
		t.Fatal("PlainHTTPUnrestricted still restricts plain HTTP")
	}
}

func TestDialTriesResolvedAddressesWithinOneTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closedPort, err := net.SplitHostPort(closed.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	listed, err := ParseAllowlist(
		[]string{"127.0.0.1:" + port, "[::1]:" + port, "127.0.0.1:" + closedPort, "192.0.2.0/24"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{
		Timeout:  300 * time.Millisecond,
		Resolver: fakeResolver(net.ParseIP("10.0.9.9"), net.IPv6loopback, net.ParseIP("127.0.0.1")),
	}
	conn, err := dial(context.Background(), dialer, "tcp", "fallback.test:"+port, false, listed)
	if err != nil {
		t.Fatalf("dial after unlisted and refused addresses: %v", err)
	}
	_ = conn.Close()

	dialer.Resolver = fakeResolver(net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.1"))
	if _, err := dial(context.Background(), dialer, "tcp", "refused.test:"+closedPort, false, listed); err == nil ||
		errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("refused listed address after an unlisted one: error = %v, want the connection error", err)
	}

	var budgets []time.Duration
	dialer.Timeout = 6 * time.Second
	dialer.Resolver = fakeResolver(
		net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2"), net.ParseIP("192.0.2.3"), net.ParseIP("192.0.2.4"),
	)
	dialer.ControlContext = func(ctx context.Context, _, _ string, _ syscall.RawConn) error {
		deadline, _ := ctx.Deadline()
		budgets = append(budgets, time.Until(deadline).Round(time.Second))
		return errors.New("unreachable")
	}
	if _, err := dial(context.Background(), dialer, "tcp", "unreachable.test:80", false, listed); err == nil {
		t.Fatal("dial succeeded through a failing hook")
	}
	want := []time.Duration{2 * time.Second, 2 * time.Second, 3 * time.Second, 6 * time.Second}
	if !slices.Equal(budgets, want) {
		t.Fatalf("attempt budgets = %v, want %v shared from one %v timeout", budgets, want, dialer.Timeout)
	}

	synctest.Test(t, func(t *testing.T) {
		budgets = nil
		dialer.Resolver = fakeResolver(net.ParseIP("192.0.2.1"))
		answer := dialer.Resolver.Dial
		dialer.Resolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			time.Sleep(time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
			return answer(ctx, network, address)
		}
		if _, err := dial(context.Background(), dialer, "tcp", "slow-dns.test:80", false, listed); err == nil {
			t.Fatal("dial succeeded through a failing hook")
		}
		if len(budgets) != 1 || budgets[0] >= dialer.Timeout {
			t.Fatalf("attempt budget after a slow lookup = %v, want DNS time taken from the %v timeout", budgets, dialer.Timeout)
		}
	})
}

func fakeResolver(ips ...net.IP) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go answerDNS(server, ips)
		return client, nil
	}}
}

func answerDNS(conn net.Conn, ips []net.IP) {
	defer func() { _ = conn.Close() }()
	for {
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(query)
		if err != nil {
			return
		}
		question, err := parser.Question()
		if err != nil {
			return
		}
		answer := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true},
			Questions: []dnsmessage.Question{question},
		}
		for _, ip := range ips {
			resource := dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET}
			switch v4 := ip.To4(); {
			case question.Type == dnsmessage.TypeA && v4 != nil:
				answer.Answers = append(answer.Answers, dnsmessage.Resource{
					Header: resource, Body: &dnsmessage.AResource{A: [4]byte(v4)},
				})
			case question.Type == dnsmessage.TypeAAAA && v4 == nil:
				answer.Answers = append(answer.Answers, dnsmessage.Resource{
					Header: resource, Body: &dnsmessage.AAAAResource{AAAA: [16]byte(ip.To16())},
				})
			}
		}
		packed, err := answer.Pack()
		if err != nil {
			return
		}
		binary.BigEndian.PutUint16(size[:], uint16(len(packed)))
		if _, err := conn.Write(append(size[:], packed...)); err != nil {
			return
		}
	}
}

func TestPlainHTTPMarkerReachesDial(t *testing.T) {
	type mark struct {
		policy PlainHTTPPolicy
		marked bool
	}
	var seen []mark
	marker := plainHTTPMarker{policy: PlainHTTPLoopbackOnly, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			policy, marked := ctx.Value(plainHTTPKey{}).(PlainHTTPPolicy)
			seen = append(seen, mark{policy: policy, marked: marked})
			return nil, errors.New("stop")
		},
	}}
	client := &http.Client{Transport: marker}
	for _, rawURL := range []string{"http://example.test/", "https://example.test/"} {
		if response, err := client.Get(rawURL); err == nil {
			_ = response.Body.Close()
		}
	}
	if want := []mark{{policy: PlainHTTPLoopbackOnly, marked: true}, {}}; !slices.Equal(seen, want) {
		t.Fatalf("plain HTTP marks seen at dial = %+v, want %+v", seen, want)
	}
}
