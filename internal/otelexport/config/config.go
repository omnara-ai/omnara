// Package config validates OpenTelemetry exporter settings.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"golang.org/x/net/http/httpguts"
)

const (
	MaxHeaders           = 20
	MaxSampleBasisPoints = 10000
)

// Settings is one exporter's destination. Endpoint is the full OTLP/HTTP
// traces URL and is used exactly as given. SecretHeaders maps a header name to
// the generic secret holding its value.
type Settings struct {
	Endpoint          string
	Variant           oteltrace.Variant
	Headers           map[string]string
	SecretHeaders     map[string]uuid.UUID
	SampleBasisPoints int32
}

// reservedHeaders are set by the sender and cannot be overridden.
var reservedHeaders = map[string]bool{"content-type": true, "content-length": true, "host": true}

func Validate(settings Settings, endpointAllowlist *ssrf.Allowlist) (Settings, error) {
	endpoint, err := webhookconfig.ValidateEndpointURL(settings.Endpoint, endpointAllowlist, "exporter endpoint")
	if err != nil {
		return Settings{}, err
	}
	settings.Endpoint = endpoint
	variant, ok := oteltrace.ParseVariant(string(settings.Variant))
	if !ok {
		return Settings{}, errors.New("unsupported exporter variant")
	}
	settings.Variant = variant
	if settings.SampleBasisPoints < 0 || settings.SampleBasisPoints > MaxSampleBasisPoints {
		return Settings{}, errors.New("sample_percent must be between 0 and 100")
	}
	if settings.Headers == nil {
		settings.Headers = map[string]string{}
	}
	if settings.SecretHeaders == nil {
		settings.SecretHeaders = map[string]uuid.UUID{}
	}
	if len(settings.Headers)+len(settings.SecretHeaders) > MaxHeaders {
		return Settings{}, fmt.Errorf("headers and secret_headers may contain at most %d entries combined", MaxHeaders)
	}
	seen := make(map[string]bool, len(settings.Headers)+len(settings.SecretHeaders))
	validateName := func(name string) error {
		lower := strings.ToLower(name)
		switch {
		case !httpguts.ValidHeaderFieldName(name):
			return fmt.Errorf("invalid header name %q", name)
		case reservedHeaders[lower]:
			return fmt.Errorf("header %s is reserved", name)
		case seen[lower]:
			return fmt.Errorf("header %s is set more than once", name)
		}
		seen[lower] = true
		return nil
	}
	for name, value := range settings.Headers {
		if err := validateName(name); err != nil {
			return Settings{}, err
		}
		if err := ValidateHeaderValue(name, value); err != nil {
			return Settings{}, err
		}
	}
	for name, secretID := range settings.SecretHeaders {
		if err := validateName(name); err != nil {
			return Settings{}, err
		}
		if secretID == uuid.Nil {
			return Settings{}, fmt.Errorf("secret header %s requires a secret", name)
		}
	}
	return settings, nil
}

func ValidateHeaderValue(name, value string) error {
	if value == "" || len(value) > 8192 || !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("header %s has an invalid value", name)
	}
	return nil
}
