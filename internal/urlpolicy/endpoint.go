package urlpolicy

import (
	"errors"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

// ValidateEndpointURL normalizes a customer-supplied delivery endpoint; label
// names it in the errors.
func ValidateEndpointURL(raw string, endpointAllowlist *ssrf.Allowlist, label string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || (parsed.Scheme != "https" &&
		(parsed.Scheme != "http" || !PermitsPlainHTTP(parsed, endpointAllowlist))) {
		return "", errors.New(label + " must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New(label + " must not contain credentials or a fragment")
	}
	host := ssrf.ClassifyURLHost(parsed.Hostname())
	port := ssrf.URLPort(parsed)
	if (host.IP == nil && host.LocalDev && !endpointAllowlist.PermitsLocalhost(port)) ||
		(host.IP != nil && !endpointAllowlist.PermitsIP(host.IP, port, false)) {
		return "", errors.New(label + " must target a public host")
	}
	normalized := parsed.String()
	if len(normalized) > 2048 {
		return "", errors.New(label + " must not exceed 2048 bytes")
	}
	return normalized, nil
}
