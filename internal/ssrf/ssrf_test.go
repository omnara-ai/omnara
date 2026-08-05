package ssrf

import (
	"net"
	"testing"
)

func TestIsAllowedIPBlocksSpecialUseRanges(t *testing.T) {
	tests := []struct {
		name          string
		ip            string
		allowLoopback bool
		want          bool
	}{
		{name: "public ipv4", ip: "8.8.8.8", want: true},
		{name: "public ipv6", ip: "2001:4860:4860::8888", want: true},
		{name: "loopback ipv4 blocked", ip: "127.0.0.1"},
		{name: "loopback ipv4 allowed in dev", ip: "127.0.0.1", allowLoopback: true, want: true},
		{name: "loopback ipv6 blocked", ip: "::1"},
		{name: "loopback ipv6 allowed in dev", ip: "::1", allowLoopback: true, want: true},
		{name: "ipv4-compatible loopback blocked", ip: "::127.0.0.1"},
		{name: "ipv4-compatible loopback allowed in dev", ip: "::127.0.0.1", allowLoopback: true, want: true},
		{name: "unspecified ipv4", ip: "0.0.0.0"},
		{name: "unspecified ipv6", ip: "::"},
		{name: "private ipv4 10", ip: "10.0.0.1"},
		{name: "private ipv4 172", ip: "172.16.0.1"},
		{name: "private ipv4 192", ip: "192.168.1.1"},
		{name: "private ipv6 ula", ip: "fc00::1"},
		{name: "link local ipv4 metadata", ip: "169.254.169.254"},
		{name: "link local ipv6", ip: "fe80::1"},
		{name: "multicast ipv4", ip: "224.0.0.1"},
		{name: "multicast ipv6", ip: "ff02::1"},
		{name: "cgnat ipv4", ip: "100.64.0.1"},
		{name: "current network ipv4", ip: "0.1.2.3"},
		{name: "ipv4 protocol assignments", ip: "192.0.0.1"},
		{name: "deprecated 6to4 relay anycast", ip: "192.88.99.1"},
		{name: "benchmark ipv4", ip: "198.18.0.1"},
		{name: "test net 1", ip: "192.0.2.1"},
		{name: "test net 2", ip: "198.51.100.1"},
		{name: "test net 3", ip: "203.0.113.1"},
		{name: "reserved ipv4", ip: "240.0.0.1"},
		{name: "local nat64 ipv6", ip: "64:ff9b:1::1"},
		{name: "discard-only ipv6", ip: "100::1"},
		{name: "dummy ipv6", ip: "100:0:0:1::1"},
		{name: "unassigned ietf protocol ipv6", ip: "2001:5::1"},
		{name: "benchmark ipv6", ip: "2001:2::1"},
		{name: "deprecated orchid ipv6", ip: "2001:10::1"},
		{name: "amt ipv6", ip: "2001:3::1"},
		{name: "orchid v2 ipv6", ip: "2001:20::1"},
		{name: "deprecated site local ipv6", ip: "fec0::1"},
		{name: "documentation ipv6", ip: "2001:db8::1"},
		{name: "documentation ipv6 2024", ip: "3fff::1"},
		{name: "segment routing ipv6", ip: "5f00::1"},
		{name: "ipv4 mapped private", ip: "::ffff:10.0.0.1"},
		{name: "ipv4 mapped public", ip: "::ffff:8.8.8.8", want: true},
		{name: "ipv4-compatible metadata", ip: "::169.254.169.254"},
		{name: "ipv4-compatible public", ip: "::8.8.8.8", want: true},
		{name: "nat64 embedded metadata ipv4", ip: "64:ff9b::169.254.169.254"},
		{name: "nat64 embedded public ipv4", ip: "64:ff9b::8.8.8.8", want: true},
		{name: "6to4 embedded loopback ipv4", ip: "2002:7f00:1::"},
		{name: "6to4 embedded public ipv4", ip: "2002:0808:0808::", want: true},
		{name: "teredo embedded test-net client ipv4", ip: "2001:0000:4136:e378:8000:63bf:3fff:fdd2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("parse %s", tc.ip)
			}
			if got := IsAllowedIP(ip, tc.allowLoopback); got != tc.want {
				t.Fatalf("IsAllowedIP(%s, %v) = %v, want %v", tc.ip, tc.allowLoopback, got, tc.want)
			}
		})
	}
	if IsAllowedIP(nil, false) {
		t.Fatal("invalid IP accepted")
	}
}
