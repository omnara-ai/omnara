package ssrf

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
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

func IsAllowedIP(ip net.IP, allowLoopback bool) bool {
	addr, ok := canonicalAddr(ip)
	if !ok {
		return false
	}
	if addr.IsLoopback() {
		return allowLoopback
	}
	return classifyAddr(addr) == classPublic
}

type addressClass uint8

const (
	classPrivate addressClass = iota
	classPublic
	classNever
)

var classPrefixes = [...][]netip.Prefix{
	classPublic: parsePrefixes("0.0.0.0/0", "2000::/3"),
	classPrivate: parsePrefixes(
		"::/0", "::1/128", "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2600:f0f0::/96", "3fff::/20",
	),
	classNever: parsePrefixes(
		"169.254.0.0/16", "100.100.100.200/32", "168.63.129.16/32", "fe80::/10", "fd00:ec2::254/128",
		"fd00:ec2::23/128", "fd20:ce::254/128", "fd00:c1::a9fe:a9fe/128", "64:ff9b:1::/48", "::/96",
		"::ffff:0:0:0/96", "2001::/32", "2002::/16",
	),
}

var nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")

func parsePrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}

func canonicalAddr(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	if nat64WellKnown.Contains(addr) {
		raw := addr.As16()
		addr = netip.AddrFrom4([4]byte(raw[12:]))
	}
	return addr, ok
}

func classifyAddr(addr netip.Addr) addressClass {
	return classifyPrefix(netip.PrefixFrom(addr, addr.BitLen()))
}

func classifyPrefix(target netip.Prefix) addressClass {
	class, longest := classPrivate, -1
	for candidate, prefixes := range classPrefixes {
		for _, prefix := range prefixes {
			if prefix.Bits() > longest && covers(prefix, target) {
				class, longest = addressClass(candidate), prefix.Bits()
			}
		}
	}
	return class
}

func whollyNever(target netip.Prefix) bool {
	inside := func(prefix netip.Prefix) bool { return covers(target, prefix) }
	return classifyPrefix(target) == classNever &&
		!slices.ContainsFunc(classPrefixes[classPrivate], inside) &&
		!slices.ContainsFunc(classPrefixes[classPublic], inside)
}

func covers(outer, inner netip.Prefix) bool {
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}
