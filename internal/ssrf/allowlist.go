package ssrf

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync/atomic"
)

type Allowlist struct {
	setting            string
	plainHTTPHostnames bool
	entries            atomic.Pointer[[]allowEntry]
}

type allowEntry struct {
	prefix netip.Prefix
	port   uint16
}

var (
	Endpoints = &Allowlist{setting: "OMNARA_PRIVATE_ENDPOINT_CIDRS", plainHTTPHostnames: true}
	WebFetch  = &Allowlist{setting: "OMNARA_WEB_FETCH_PRIVATE_CIDRS"}
)

var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("fd00:ec2::254"),
	netip.MustParseAddr("100.100.100.200"),
}

func Configure(endpoints, webFetch []string) error {
	for _, list := range []struct {
		allowlist *Allowlist
		raw       []string
	}{{Endpoints, endpoints}, {WebFetch, webFetch}} {
		entries := make([]allowEntry, 0, len(list.raw))
		for _, raw := range list.raw {
			entry, err := parseAllowEntry(raw)
			if err != nil {
				return fmt.Errorf("parse %s: %w", list.allowlist.setting, err)
			}
			entries = append(entries, entry)
		}
		list.allowlist.entries.Store(&entries)
	}
	return nil
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
	return allowEntry{prefix: prefix.Masked(), port: uint16(port)}, nil
}

func (a *Allowlist) PermitsIP(ip net.IP, allowLoopback bool) bool {
	return a.contains(ip, 0) || IsAllowedIP(ip, allowLoopback)
}

func (a *Allowlist) Configured() bool {
	entries := a.entries.Load()
	return entries != nil && len(*entries) > 0
}

func (a *Allowlist) PermitsPlainHTTP(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return a.contains(ip, 0)
	}
	return a.plainHTTPHostnames && host != "" && a.Configured()
}

func (a *Allowlist) permits(ip net.IP, port uint16, allowLoopback, plainHTTP bool) bool {
	if a.contains(ip, port) {
		return true
	}
	if plainHTTP {
		return allowLoopback && ip.IsLoopback()
	}
	return IsAllowedIP(ip, allowLoopback)
}

func (a *Allowlist) contains(ip net.IP, port uint16) bool {
	if a == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	if !ok || isMetadata(ip) {
		return false
	}
	entries := a.entries.Load()
	if entries == nil {
		return false
	}
	for _, entry := range *entries {
		if entry.prefix.Contains(addr) && (entry.port == 0 || port == 0 || entry.port == port) {
			return true
		}
	}
	return false
}

func isMetadata(ip net.IP) bool {
	return slices.ContainsFunc(append(embeddedIPv4Addresses(ip), ip), func(candidate net.IP) bool {
		addr, ok := netip.AddrFromSlice(candidate)
		return ok && slices.Contains(metadataAddrs, addr.Unmap())
	})
}

func (a *Allowlist) blocked(host, port string, literal, plainHTTP bool) error {
	listed := a != nil && a.Configured()
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
