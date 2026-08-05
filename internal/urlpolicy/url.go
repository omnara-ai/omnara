package urlpolicy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

func RequireHTTPSOrLoopback(rawURL string) error {
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
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") && loopbackHost(parsed.Hostname()) {
		return nil
	}
	return errors.New("URL must use HTTPS unless it uses a loopback host")
}

func loopbackHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
