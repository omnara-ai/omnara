package agentconfig

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func ValidateEventWebhookURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", errors.New("event webhook URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("event webhook URL must not contain credentials or a fragment")
	}
	host := classifyURLHost(parsed.Hostname())
	if host.LocalDev || (host.IP != nil && !ssrf.IsAllowedIP(host.IP, false)) {
		return "", errors.New("event webhook URL must target a public host")
	}
	return parsed.String(), nil
}

func DecodeEventWebhookSigningKey(secret string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(key) < 24 || len(key) > 64 {
		return nil, errors.New("event webhook signing secret must contain a base64-encoded 24–64 byte key")
	}
	return key, nil
}
