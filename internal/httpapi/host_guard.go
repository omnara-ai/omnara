package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/httporigin"
)

type configuredOrigin struct {
	scheme string
	host   string
}

func parseConfiguredOrigin(raw string) (configuredOrigin, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return configuredOrigin{}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return configuredOrigin{}, fmt.Errorf("invalid public URL %q", raw)
	}
	scheme := strings.ToLower(parsed.Scheme)
	return configuredOrigin{scheme: scheme, host: httporigin.CanonicalHost(scheme, parsed.Host)}, nil
}

func (o configuredOrigin) matchesHost(rawHost string) bool {
	if o.host == "" {
		return true
	}
	return httporigin.CanonicalHost(o.scheme, rawHost) == o.host
}

func isLocalOnlyHost(rawHost string) bool {
	host := rawHost
	if splitHost, _, err := net.SplitHostPort(rawHost); err == nil {
		host = splitHost
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" || host == "host.docker.internal" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) publicHostGuard(next http.Handler) http.Handler {
	if len(s.publicOrigins) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, origin := range s.publicOrigins {
			if origin.matchesHost(r.Host) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if s.allowInsecureLocalHostBypass && isLocalOnlyHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		s.notFound(w, r)
	})
}
