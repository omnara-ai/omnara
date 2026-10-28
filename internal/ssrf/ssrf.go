package ssrf

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
)

var ErrBlockedAddress = errors.New("ssrf: address blocked")

type URLHost struct {
	IP       net.IP
	LocalDev bool
}

func ClassifyURLHost(host string) URLHost {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	ip := net.ParseIP(normalized)
	return URLHost{
		IP:       ip,
		LocalDev: normalized == "localhost" || (ip != nil && ip.IsLoopback()),
	}
}

// allowedPrefixes is the operator-managed egress allowlist configured via
// OMNARA_ALLOWED_PRIVATE_CIDRS. It is installed once at process startup via
// SetAllowedPrefixes and consulted by every policy check, including the
// dial-time check in Dial, so DNS rebinding stays blocked: every connection
// re-resolves and is re-checked against the same prefixes. Held atomically so
// reads on the request path can never race a (re)configuration.
var allowedPrefixes atomic.Pointer[[]netip.Prefix]

// SetAllowedPrefixes installs the operator-managed network allowlist for the
// whole process. Call it once during startup before any outbound traffic.
func SetAllowedPrefixes(prefixes []netip.Prefix) {
	if prefixes == nil {
		allowedPrefixes.Store(nil)
		return
	}
	allowedPrefixes.Store(&prefixes)
}

func loadAllowedPrefixes() []netip.Prefix {
	if p := allowedPrefixes.Load(); p != nil {
		return *p
	}
	return nil
}

// IsAllowedPrefix reports whether an IP literal falls inside one of the
// operator-allowlisted prefixes, including IPv4 embedded in IPv6 forms. It is
// meant for save-time host classification; the authoritative enforcement point
// stays in Dial, which checks resolved IPs on every connection.
func IsAllowedPrefix(ip net.IP) bool {
	return isAllowedPrefixIn(loadAllowedPrefixes(), ip)
}

func isAllowedPrefixIn(prefixes []netip.Prefix, ip net.IP) bool {
	if len(prefixes) == 0 {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if ip.To16() == nil {
		return false
	}
	return prefixesAllowIP(prefixes, ip) || embeddedPrefixesAllowIP(prefixes, ip)
}

func IsAllowedIP(ip net.IP, allowLoopback bool) bool {
	prefixes := loadAllowedPrefixes()
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if ip.To16() == nil {
		return false
	}
	if isAllowedPrefixIn(prefixes, ip) {
		return true
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

// HostPermitsPlainHTTP is the save-time gate for plain HTTP on operator-owned
// URLs (model provider base URLs, MCP endpoints, event webhooks). A literal IP
// must sit inside the operator allowlist. Other hostnames are accepted whenever
// an allowlist is configured, deferring to dial-time enforcement: the API and
// worker may see different DNS views for internal names, so save-time resolution
// would fight the dial gate or break registration outright. Dial re-resolves and
// re-checks every connection, so a name that later resolves outside the allowlist
// cannot reach blocked ranges.
func HostPermitsPlainHTTP(host string) bool {
	prefixes := loadAllowedPrefixes()
	if len(prefixes) == 0 {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return isAllowedPrefixIn(prefixes, ip)
	}
	return true
}

// LiteralIPInAllowedPrefix reports whether a hostname is a literal IP inside the
// operator allowlist. Unlike HostPermitsPlainHTTP it accepts no hostnames, which
// makes it the right gate for URLs that operators do not control (web fetch
// targets chosen by models).
func LiteralIPInAllowedPrefix(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && IsAllowedPrefix(ip)
}

func prefixesAllowIP(prefixes []netip.Prefix, ip net.IP) bool {
	for _, prefix := range prefixes {
		if cidrContains(prefix, ip) {
			return true
		}
	}
	return false
}

func embeddedPrefixesAllowIP(prefixes []netip.Prefix, ip net.IP) bool {
	for _, embedded := range embeddedIPv4Addresses(ip) {
		if prefixesAllowIP(prefixes, embedded) {
			return true
		}
	}
	return false
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
