package ssrf

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
)

type Allowlist struct {
	entries []allowEntry
}

type allowEntry struct {
	prefix netip.Prefix
	port   uint16
}

var alwaysBlocked = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd00:ec2::23/128"),
	netip.MustParsePrefix("fd20:ce::254/128"),
	netip.MustParsePrefix("fd00:c1::a9fe:a9fe/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func ParseAllowlist(raw []string) (*Allowlist, error) {
	list := &Allowlist{}
	for _, value := range raw {
		entry, err := parseAllowEntry(value)
		if err != nil {
			return nil, err
		}
		list.entries = append(list.entries, entry)
	}
	return list, nil
}

func parseAllowEntry(raw string) (allowEntry, error) {
	target, port := raw, uint64(0)
	if host, rawPort, err := net.SplitHostPort(raw); err == nil {
		port, err = strconv.ParseUint(rawPort, 10, 16)
		if err != nil || port == 0 {
			return allowEntry{}, fmt.Errorf("invalid port in %q", raw)
		}
		target = host
	}
	prefix, err := netip.ParsePrefix(target)
	if err != nil {
		addr, addrErr := netip.ParseAddr(target)
		if addrErr != nil || addr.Zone() != "" {
			return allowEntry{}, fmt.Errorf("%q is not an IP address or CIDR range", raw)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if prefix.Addr().Is4In6() {
		return allowEntry{}, fmt.Errorf("%q must use the IPv4 form", raw)
	}
	prefix = prefix.Masked()
	if alwaysBlockedPrefix(prefix) {
		return allowEntry{}, fmt.Errorf("%q is always blocked and cannot be allowed", raw)
	}
	return allowEntry{prefix: prefix, port: uint16(port)}, nil
}

func alwaysBlockedPrefix(prefix netip.Prefix) bool {
	return slices.ContainsFunc(alwaysBlocked, func(blocked netip.Prefix) bool {
		return blocked.Bits() <= prefix.Bits() && blocked.Contains(prefix.Addr())
	}) || (prefix.IsSingleIP() && isAlwaysBlocked(prefix.Addr().AsSlice()))
}

func (a *Allowlist) PermitsIP(ip net.IP, port uint16, allowLoopback bool) bool {
	return a.Contains(ip, port) || IsAllowedIP(ip, allowLoopback)
}

func (a *Allowlist) PermitsLocalhost(port uint16) bool {
	return a.Contains(net.IPv4(127, 0, 0, 1), port) || a.Contains(net.IPv6loopback, port)
}

func (a *Allowlist) Configured() bool {
	return a != nil && len(a.entries) > 0
}

func URLPort(u *url.URL) uint16 {
	raw := u.Port()
	if raw == "" {
		raw = "443"
		if u.Scheme == "http" {
			raw = "80"
		}
	}
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(port)
}

func (a *Allowlist) Contains(ip net.IP, port uint16) bool {
	if a == nil || port == 0 {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	if !ok || isAlwaysBlocked(ip) {
		return false
	}
	for _, entry := range a.entries {
		if entry.prefix.Contains(addr) && (entry.port == 0 || entry.port == port) {
			return true
		}
	}
	return false
}

func isAlwaysBlocked(ip net.IP) bool {
	return slices.ContainsFunc(append(embeddedIPv4Addresses(ip), ip), func(candidate net.IP) bool {
		addr, ok := netip.AddrFromSlice(candidate)
		return ok && slices.ContainsFunc(alwaysBlocked, func(prefix netip.Prefix) bool {
			return prefix.Contains(addr.Unmap())
		})
	})
}

func (a *Allowlist) blocked(host, port string, literal, plainHTTP bool) error {
	listed := a.Configured()
	switch {
	case !listed && literal:
		return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
	case !listed:
		return fmt.Errorf("%w: %s (no allowed IPs)", ErrBlockedAddress, host)
	case plainHTTP:
		return fmt.Errorf(
			"%w: plain HTTP to %s needs an allowed private address", ErrBlockedAddress, net.JoinHostPort(host, port),
		)
	default:
		return fmt.Errorf("%w: %s is not an allowed private address", ErrBlockedAddress, net.JoinHostPort(host, port))
	}
}
