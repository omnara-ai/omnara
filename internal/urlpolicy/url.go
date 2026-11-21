package urlpolicy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func RequireHTTPSOrLoopback(rawURL string) error {
	return RequireHTTPSOrPrivateEndpoint(rawURL, nil)
}

func RequireHTTPSOrPrivateEndpoint(rawURL string, endpointAllowlist *ssrf.Allowlist) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Hostname() == "" || parsed.Opaque != "" {
		return errors.New("URL must be absolute and include a host")
	}
	if parsed.User != nil {
		return errors.New("URL must not include user information")
	}
	if parsed.Fragment != "" {
		return errors.New("URL must not include a fragment")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("URL must include a valid port")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("URL must include a valid port")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") &&
		(loopbackHost(parsed.Hostname()) || PermitsPlainHTTP(parsed, endpointAllowlist)) {
		return nil
	}
	return errors.New("URL must use HTTPS")
}

func PermitsPlainHTTP(u *url.URL, endpointAllowlist *ssrf.Allowlist) bool {
	host := ssrf.ClassifyURLHost(u.Hostname())
	switch {
	case host.IP != nil:
		return endpointAllowlist.Contains(host.IP, ssrf.URLPort(u))
	case !endpointAllowlist.Configured():
		return false
	case host.LocalDev:
		return endpointAllowlist.PermitsLocalhost(ssrf.URLPort(u))
	default:
		return u.Hostname() != ""
	}
}

func loopbackHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
