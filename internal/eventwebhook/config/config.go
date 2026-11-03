package config

import (
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

type Settings struct {
	URL             string
	EventTypes      []string
	SigningSecretID uuid.UUID
}

func Validate(settings Settings) (Settings, error) {
	url, err := ValidateURL(settings.URL)
	if err != nil {
		return Settings{}, err
	}
	if len(settings.EventTypes) == 0 {
		return Settings{}, errors.New("at least one event type is required")
	}
	for i, eventType := range settings.EventTypes {
		switch eventType {
		case "agent_input", "model_output", "tool_result", "context_checkpoint", "tool_call_update":
		default:
			return Settings{}, errors.New("unsupported event type")
		}
		if slices.Contains(settings.EventTypes[:i], eventType) {
			return Settings{}, errors.New("event types must be unique")
		}
	}
	settings.URL = url
	return settings, nil
}

func ValidateURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", errors.New("event webhook URL must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("event webhook URL must not contain credentials or a fragment")
	}
	host := ssrf.ClassifyURLHost(parsed.Hostname())
	if host.LocalDev || (host.IP != nil && !ssrf.IsAllowedIP(host.IP, false)) {
		return "", errors.New("event webhook URL must target a public host")
	}
	normalized := parsed.String()
	if len(normalized) > 2048 {
		return "", errors.New("event webhook URL must not exceed 2048 bytes")
	}
	return normalized, nil
}

func DecodeSigningKey(secret string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(key) < 24 || len(key) > 64 {
		return nil, errors.New("event webhook signing secret must contain a base64-encoded 24–64 byte key")
	}
	return key, nil
}
