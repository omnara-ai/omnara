package ssrf

import (
	"errors"
	"net"
	"net/netip"
)

var ErrBlockedAddress = errors.New("ssrf: address blocked")

func IsAllowedIP(ip net.IP, allowLoopback bool) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if ip.To16() == nil {
		return false
	}
	if ip.IsLoopback() {
		return allowLoopback
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if !embeddedIPv4Allowed(ip, allowLoopback) {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if cidrContains(prefix, ip) {
			return false
		}
	}
	return true
}

// blockedPrefixes supplements net.IP's broad classifications with a
// conservative snapshot of special-purpose ranges that untrusted outbound
// requests must not reach. Keep it synchronized with the IANA registries:
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

var (
	ipv4CompatibleIPv6 = netip.MustParsePrefix("::/96")
	nat64WellKnownIPv6 = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourIPv6      = netip.MustParsePrefix("2002::/16")
)

func embeddedIPv4Allowed(ip net.IP, allowLoopback bool) bool {
	if ip.To4() != nil {
		return true
	}
	ip = ip.To16()
	if ip == nil {
		return false
	}
	for _, embedded := range embeddedIPv4Addresses(ip) {
		if !IsAllowedIP(embedded, allowLoopback) {
			return false
		}
	}
	return true
}

func embeddedIPv4Addresses(ip net.IP) []net.IP {
	switch {
	case cidrContains(ipv4CompatibleIPv6, ip):
		return []net.IP{net.IPv4(ip[12], ip[13], ip[14], ip[15])}
	case cidrContains(nat64WellKnownIPv6, ip):
		return []net.IP{net.IPv4(ip[12], ip[13], ip[14], ip[15])}
	case cidrContains(sixToFourIPv6, ip):
		return []net.IP{net.IPv4(ip[2], ip[3], ip[4], ip[5])}
	default:
		return nil
	}
}

func cidrContains(prefix netip.Prefix, ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	return ok && prefix.Contains(address)
}
