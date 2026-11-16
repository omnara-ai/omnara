package config

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/urlpolicy"
)

type Settings struct {
	URL             string
	EventTypes      []string
	SigningSecretID uuid.UUID
}

func Validate(settings Settings, endpointAllowlist *ssrf.Allowlist) (Settings, error) {
	url, err := ValidateURL(settings.URL, endpointAllowlist)
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

func ValidateURL(raw string, endpointAllowlist *ssrf.Allowlist) (string, error) {
	return urlpolicy.ValidateEndpointURL(raw, endpointAllowlist, "event webhook URL")
}

func DecodeSigningKey(secret string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(key) < 24 || len(key) > 64 {
		return nil, errors.New("event webhook signing secret must contain a base64-encoded 24–64 byte key")
	}
	return key, nil
}
