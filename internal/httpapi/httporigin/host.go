package httporigin

import (
	"net"
	"strings"
)

func CanonicalHost(scheme, rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return ""
	}
	hostname := host
	port := ""
	if splitHost, splitPort, err := net.SplitHostPort(host); err == nil {
		hostname = splitHost
		port = splitPort
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		hostname = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	port = strings.TrimSpace(port)
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port == "" {
		return hostname
	}
	return net.JoinHostPort(hostname, port)
}
